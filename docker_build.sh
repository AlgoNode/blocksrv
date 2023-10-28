#!/bin/bash

#static 3MB build, no distro, no shell :)
docker build . -t urtho/blocksrv:latest
docker push urtho/blocksrv:latest
