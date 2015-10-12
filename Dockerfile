FROM centurylink/ca-certs
ADD kube2consul kube2consul
ENTRYPOINT ["/kube2consul"]
