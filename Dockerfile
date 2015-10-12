FROM centurylink/ca-certs
COPY kube2consul /
CMD /kube2consul
