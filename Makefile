.PHONY: all kube2consul clean test

all: kube2consul

kube2consul: kube2consul.go
	CGO_ENABLED=0 godep go build -a -tags netgo --ldflags '-w' ./kube2consul.go

clean:
	rm -f kube2consul

test: clean
	godep go test -v --vmodule=*=4
