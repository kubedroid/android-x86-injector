GOPATH=$(shell pwd)

docker:
	sudo docker build --rm=false . -t quay.io/quamotion/android-x86-injector:v0.1

run:
	sudo docker run --rm -it quay.io/quamotion/android-x86-injector:v0.1 /bin/bash

test:
	echo $(GOPATH)
	cd src/android-x86-injector && dep ensure -vendor-only
	cd src/android-x86-injector && go test
