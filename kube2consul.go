/*
Copyright 2014 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// kube2consul is a bridge between Kubernetes and Consul.  It watches the
// Kubernetes master for changes in Services and creates new DNS records on the
// consul agent.

package main // import "github.com/CAP-ALL/kube2consul"

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/github.com/golang/glog"
	consulapi "github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/github.com/hashicorp/consul/api"
	kapi "github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/api"
	kcache "github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/client/cache"
	kclient "github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/client/unversioned"
	kclientcmd "github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
	kcontrollerFramework "github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/controller/framework"
	kframework "github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/controller/framework"
	kSelector "github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/fields"
	"github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/util"

	"github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/api"
	"github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/fields"
	"github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/labels"
)

var (
	argConsulAgent   = flag.String("consul-agent", "http://127.0.0.1:8500", "URL to consul agent")
	argKubecfgFile   = flag.String("kubecfg_file", "", "Location of kubecfg file for access to kubernetes service")
	argKubeMasterUrl = flag.String("kube_master_url", "https://${KUBERNETES_SERVICE_HOST}:${KUBERNETES_SERVICE_PORT}", "Url to reach kubernetes master. Env variables in this flag will be expanded.")
)

const (
	// Maximum number of attempts to connect to consul server.
	maxConnectAttempts = 12
	// Resync period for the kube controller loop.
	resyncPeriod = 5 * time.Second
)

type nodeInformation struct {
	name string

	address string

	ready bool
	// map[service] DNS IDs
	ids map[string][]string
}

type kube2consul struct {
	// Consul client.
	consulClient *consulapi.Client

	// DNS domain name.
	domain string

	//Nodes Name / valid
	nodes map[string]nodeInformation

	//All Services.
	services map[string]*kapi.Service
}

func Newkube2consul() *kube2consul {
	var k kube2consul
	k.nodes = make(map[string]nodeInformation)
	k.services = make(map[string]*kapi.Service)

	return &k
}

func NewnodeInformation() *nodeInformation {
	var n nodeInformation
	n.ready = false
	n.ids = make(map[string][]string)

	return &n
}

func Contains(s []string, e string) bool {
	for _, i := range s {
		if i == e {
			return true
		}
	}
	return false
}

func (ks *kube2consul) removeDNS(recordID string) error {
	glog.Infof("Removing %s from DNS", recordID)
	ks.consulClient.Agent().ServiceDeregister(recordID)
	return nil
}

func (ks *kube2consul) createDNS(record string, service *kapi.Service, node *nodeInformation) error {
	if strings.Contains(record, ".") {
		glog.Infof("Service names containing '.' are not supported: %s\n", service.Name)
		return nil
	}

	// if ClusterIP is not set, do not create a DNS records
	if !kapi.IsServiceIPSet(service) {
		glog.Infof("Skipping dns record for headless service: %s\n", service.Name)
		return nil
	}

	for i := range service.Spec.Ports {
		newId := node.name + record + service.Spec.Ports[i].Name
		var asrName string

		if len(service.Spec.Ports[i].Name) > 0 {
			asrName = record + "-" + service.Spec.Ports[i].Name
		} else {
			asrName = record
		}

		asr := &consulapi.AgentServiceRegistration{
			ID:      newId,
			Name:    asrName,
			Address: node.address,
			Port:    service.Spec.Ports[i].NodePort,
			Tags:    []string{"Kube"},
		}

		if Contains(node.ids[record], newId) == false {
			glog.Infof("Setting DNS record: %v -> %v:%d\n", asr.Name, asr.Address, asr.Port)
			if err := ks.consulClient.Agent().ServiceRegister(asr); err != nil {
				return err
			}

			node.ids[record] = append(node.ids[record], newId)
		}
	}
	return nil
}

func newConsulClient(consulAgent string) (*consulapi.Client, error) {
	var (
		client *consulapi.Client
		err    error
	)

	consulConfig := consulapi.DefaultConfig()
	consulAgentUrl, err := url.Parse(consulAgent)
	if err != nil {
		glog.Infof("Error parsing Consul url")
		return nil, err
	}

	if consulAgentUrl.Host != "" {
		consulConfig.Address = consulAgentUrl.Host
	}

	if consulAgentUrl.Scheme != "" {
		consulConfig.Scheme = consulAgentUrl.Scheme
	}

	client, err = consulapi.NewClient(consulConfig)
	if err != nil {
		glog.Infof("Error creating Consul client")
		return nil, err
	}

	for attempt := 1; attempt <= maxConnectAttempts; attempt++ {
		if _, err = client.Agent().Self(); err == nil {
			break
		}

		if attempt == maxConnectAttempts {
			break
		}

		glog.Infof("[Attempt: %d] Attempting access to Consul after 5 second sleep", attempt)
		time.Sleep(5 * time.Second)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to Consul agent: %v, error: %v", consulAgent, err)
	}
	glog.Infof("Consul agent found: %v", consulAgent)

	return client, nil
}

func getKubeMasterUrl() (string, error) {
	if *argKubeMasterUrl == "" {
		return "", fmt.Errorf("no --kube_master_url specified")
	}
	parsedUrl, err := url.Parse(os.ExpandEnv(*argKubeMasterUrl))
	if err != nil {
		return "", fmt.Errorf("failed to parse --kube_master_url %s - %v", *argKubeMasterUrl, err)
	}
	if parsedUrl.Scheme == "" || parsedUrl.Host == "" || parsedUrl.Host == ":" {
		return "", fmt.Errorf("invalid --kube_master_url specified %s", *argKubeMasterUrl)
	}
	return parsedUrl.String(), nil
}

// TODO: evaluate using pkg/client/clientcmd
func newKubeClient() (*kclient.Client, error) {
	var config *kclient.Config
	masterUrl, err := getKubeMasterUrl()
	if err != nil {
		return nil, err
	}
	if *argKubecfgFile == "" {
		config = &kclient.Config{
			Host:    masterUrl,
			Version: "v1",
		}
	} else {
		var err error
		overrides := &kclientcmd.ConfigOverrides{}
		overrides.ClusterInfo.Server = masterUrl                                     // might be "", but that is OK
		rules := &kclientcmd.ClientConfigLoadingRules{ExplicitPath: *argKubecfgFile} // might be "", but that is OK
		if config, err = kclientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig(); err != nil {
			return nil, err
		}
	}
	glog.Infof("Using %s for kubernetes master", config.Host)
	glog.Infof("Using kubernetes API %s", config.Version)
	return kclient.New(config)
}

func buildNameString(service, namespace string) string {
	//glog.Infof("Name String: %s  %s", service, namespace)
	//return fmt.Sprintf("%s.%s", service, namespace)
	return fmt.Sprintf("%s", service)
}

// Returns a cache.ListWatch that gets all changes to services.
func createServiceLW(kubeClient *kclient.Client) *kcache.ListWatch {
	return kcache.NewListWatchFromClient(kubeClient, "services", kapi.NamespaceAll, kSelector.Everything())
}

// Returns a cache.ListWatch that gets all changes to services.
func createNodeLW(kubeClient *kclient.Client) *kcache.ListWatch {
	return kcache.NewListWatchFromClient(kubeClient, "nodes", kapi.NamespaceAll, kSelector.Everything())
}

func (ks *kube2consul) newService(obj interface{}) {
	if s, ok := obj.(*kapi.Service); ok {
		name := buildNameString(s.Name, s.Namespace)
		ks.services[name] = s

		glog.V(2).Info("Creating Service: ", name)
		//Add to all existing nodes
		for _, node := range ks.nodes {
			if node.ready {
				ks.createDNS(name, s, &node)
			}
		}
	}
}

func (ks *kube2consul) removeService(obj interface{}) {
	if s, ok := obj.(*kapi.Service); ok {
		name := buildNameString(s.Name, s.Namespace)
		//Remove service from node
		for _, node := range ks.nodes {
			for _, id := range node.ids[name] {
				ks.removeDNS(id)
			}
			delete(node.ids, name)
		}
		delete(ks.services, name)
	}
}

func (ks *kube2consul) updateService(oldObj, newObj interface{}) {
	if old, ok := oldObj.(*kapi.Service); ok {
		if new, ok := newObj.(*kapi.Service); ok {
			if reflect.DeepEqual(*old, *new) == false {
				ks.removeService(old)
				ks.newService(new)
			}
		}
	}
}

func (ks *kube2consul) updateNode(oldObj, newObj interface{}) {
	if n, ok := newObj.(*kapi.Node); ok {
		ready := n.Status.Conditions[0].Status == kapi.ConditionTrue
		nodeInfo := ks.nodes[n.Name]

		if nodeInfo.ready != ready {
			nodeInfo.ready = ready
			if ready {
				for serviceName, service := range ks.services {
					ks.createDNS(serviceName, service, &nodeInfo)
				}
			} else {
				for _, serviceIDs := range nodeInfo.ids {
					for _, serviceID := range serviceIDs {
						ks.removeDNS(serviceID)
					}
				}
				nodeInfo.ids = make(map[string][]string) //Clear tha map
			}
		}
	}
}

func (ks *kube2consul) newNode(newObj interface{}) {
	if node, ok := newObj.(*kapi.Node); ok {
		if _, ok := ks.nodes[node.Name]; !ok {
			glog.Info("Adding Node: ", node.Name)
			var newNode = *NewnodeInformation()
			newNode.name = node.Name
			newNode.address = node.Status.Addresses[0].Address

			ks.nodes[node.Name] = newNode
		}
	}
}

func (ks *kube2consul) removeNode(oldObj interface{}) {
	if node, ok := oldObj.(*kapi.Node); ok {
		if info, ok := ks.nodes[node.Name]; ok {
			glog.Info("Removing Node: ", node.Name)
			for _, idSet := range info.ids {
				for _, id := range idSet {
					ks.removeDNS(id)
				}
			}

			delete(ks.nodes, node.Name)
		}
	}
}

func watchForServices(kubeClient *kclient.Client, ks *kube2consul) {
	var serviceController *kcontrollerFramework.Controller
	_, serviceController = kframework.NewInformer(
		createServiceLW(kubeClient),
		&kapi.Service{},
		resyncPeriod,
		kframework.ResourceEventHandlerFuncs{
			AddFunc:    ks.newService,
			DeleteFunc: ks.removeService,
			UpdateFunc: ks.updateService,
		},
	)
	go serviceController.Run(util.NeverStop)
}

func watchForNodes(kubeClient *kclient.Client, ks *kube2consul) kcache.Store {
	store, serviceController := kframework.NewInformer(
		createNodeLW(kubeClient),
		&kapi.Node{},
		resyncPeriod,
		kframework.ResourceEventHandlerFuncs{
			AddFunc:    ks.newNode,
			DeleteFunc: ks.removeNode,
			UpdateFunc: ks.updateNode,
		},
	)
	glog.Info("About to call run!")
	go serviceController.Run(util.NeverStop)
	return store
}

func main() {
	flag.Parse()
	var err error
	// TODO: Validate input flags.
	ks := Newkube2consul()

	if ks.consulClient, err = newConsulClient(*argConsulAgent); err != nil {
		glog.Fatalf("Failed to create Consul client - %v", err)
	}

	kubeClient, err := newKubeClient()
	if err != nil {
		glog.Fatalf("Failed to create a kubernetes client: %v", err)
	}

	glog.Info(kubeClient.ServerVersion())
	glog.Info(kubeClient.Services(kapi.NamespaceAll).Get("sensu-core"))

	pods, err := kubeClient.Pods(api.NamespaceDefault).List(labels.Everything(), fields.Everything())
	if err != nil {
		for pod := range pods.Items {
			glog.Info(pod)
		}
	}

	watchForServices(kubeClient, ks)
	watchForNodes(kubeClient, ks)
	glog.Info("Watchers running")
	select {}
}
