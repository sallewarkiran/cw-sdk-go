#!/bin/bash

# This is run on Jenkins
set -e

if [ -z "${WORKSPACE}" ]
then
  echo "Jenkins WORKSPACE env var not set - exiting" >&2
  exit 1
fi

# Set up GOPATH
export GOPATH=$WORKSPACE/.go
mkdir -p $GOPATH/src/code.cryptowat.ch
mkdir -p $GOPATH/bin
export PATH=$PATH:$GOPATH/bin

ln -s -f $WORKSPACE $GOPATH/src/code.cryptowat.ch/stream-client-go
cd $GOPATH/src/code.cryptowat.ch/stream-client-go

# Set up go get creds
git config --global url."git@github.com:".insteadOf "https://github.com/"

# Install glide
curl https://glide.sh/get | sh

glide install

# Run tests
make test
