/*
Copyright 2015 The Kubernetes Authors All rights reserved.

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

package unversioned

import (
	"github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/api/meta"
	"github.com/CAP-ALL/kube2consul/Godeps/_workspace/src/k8s.io/kubernetes/pkg/apis/extensions"
)

type ScaleNamespacer interface {
	Scales(namespace string) ScaleInterface
}

// ScaleInterface has methods to work with Scale (sub)resources.
type ScaleInterface interface {
	Get(string, string) (*extensions.Scale, error)
	Update(string, *extensions.Scale) (*extensions.Scale, error)
}

// horizontalPodAutoscalers implements HorizontalPodAutoscalersNamespacer interface
type scales struct {
	client *ExperimentalClient
	ns     string
}

// newHorizontalPodAutoscalers returns a horizontalPodAutoscalers
func newScales(c *ExperimentalClient, namespace string) *scales {
	return &scales{
		client: c,
		ns:     namespace,
	}
}

// Get takes the reference to scale subresource and returns the subresource or error, if one occurs.
func (c *scales) Get(kind string, name string) (result *extensions.Scale, err error) {
	result = &extensions.Scale{}
	resource, _ := meta.KindToResource(kind, false)
	err = c.client.Get().Namespace(c.ns).Resource(resource).Name(name).SubResource("scale").Do().Into(result)
	return
}

func (c *scales) Update(kind string, scale *extensions.Scale) (result *extensions.Scale, err error) {
	result = &extensions.Scale{}
	resource, _ := meta.KindToResource(kind, false)
	err = c.client.Put().
		Namespace(scale.Namespace).
		Resource(resource).
		Name(scale.Name).
		SubResource("scale").
		Body(scale).
		Do().
		Into(result)
	return
}
