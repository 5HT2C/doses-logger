#!/bin/bash

echo "./doses-logger -n -1 -j | jq -r '.[] | .date + \",\" + .drug' | cc"
echo "for f in {1..REPLACE}; do echo \"=COUNTUNIQUE(B1:B\$f)\"; done | cc"
