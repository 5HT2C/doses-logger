#!/bin/bash

./doses-logger -n -1 -j | jq '.[] | .drug + ":" + .date' -r | awk '{ split($0,a,":"); { if (d[a[1]] == "") { d[a[1]]=a[2]; printf "%s %s\n", d[a[1]], a[1] } }}'
