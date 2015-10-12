FROM progrium/busybox
ADD kube2consul /
CMD ["/kube2consul"]
