SHELL := /bin/bash

CW_BUILD_VERSION_TAG := $(shell cat $(CW_REPO_ROOT)/tools/dev/docker-images/cw-build/version_tag)

SHELL := /bin/bash

ifndef CW_BUILD_IMAGE_VERSION
  # We're outside of the cw-build docker container, so go through docker run.
  CW_BUILD_VERSION_TAG := $(shell cat $(CW_REPO_ROOT)/tools/dev/docker-images/cw-build/version_tag)
  PROTOC_IMG=gitlab-omgwehaveit.cwix.org:5005/cryptowatch/dev/cw-build:$(CW_BUILD_VERSION_TAG)
  DOCKER_CW_REPO_ROOT=/root/cryptowatch

  PROTOC=docker run --rm -v $(CW_REPO_ROOT):$(DOCKER_CW_REPO_ROOT) \
    -w $(DOCKER_CW_REPO_ROOT)/proto -e CW_REPO_ROOT=$(DOCKER_CW_REPO_ROOT) \
    $(PROTOC_IMG)
endif

proto:
	$(PROTOC) protolock status && protoc --gofast_out=. -I. markets/*.proto
	$(PROTOC) protolock status && protoc --gofast_out=. -I. client/*.proto
	$(PROTOC) protolock status && protoc --gofast_out=Mmarkets/market.proto=code.cryptowat.ch/proto/markets,Mmarkets/pair.proto=code.cryptowat.ch/proto/markets,Mmarkets/asset.proto=code.cryptowat.ch/proto/markets,Mmarkets/index.proto=code.cryptowat.ch/proto/markets:. -I. stream/*.proto
	$(PROTOC) protolock status && protoc --gofast_out=Mstream/stream.proto=code.cryptowat.ch/proto/stream:. -I. broker/*.proto
	$(PROTOC) protolock commit
