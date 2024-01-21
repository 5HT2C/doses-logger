#!/bin/bash

# If you're looking at this script. Don't, please.
# This started as a terrible one-liner that I had to expand into multi-line form to fix a bug.
# The fix itself is hacky, (y_num C - 1), and awful.
# This is used to automatically fill past data for a spreadsheet that should have been retired by now™.
# In the future I will simply use the actual app I've been building for this, Sojourns, but this script exists for parsing and data validity reasons.

# Default random initalizer.
# This just makes for cleaner code in create-dates-year.
#EXCLUDE="$(date +%s%N | base64 | sed "s|[/+=]||g")"
# 2024-01-18 update: jesse what the FUCK is the above code for i have no idea by this point i'm so confused

if [[ -z "$1" ]]; then
    echo "Usage: ./count.sh [year]"
    exit 1
fi

if [[ ! -f "count-filter.txt" ]]; then
    echo "" > count-filter.txt
    RM_EXCLUDE_FILE=1
fi

d_num() {
    date -d "$1" +%j | sed -E "s/^[0]+//g"
}

y_num() {
    date -d "$1-01-01 + $2 days" +%Y/%m/%d
}

create-dates-year() {
    ./doses-logger -n -1 -j | jq -r '.[] | .date + "," + .drug' | rg -f therapeutic.txt -v | rg -f count-filter.txt | cut -d "," -f 1 | grep -E "^$1" | sort | uniq -c | awk '{printf "%s,%s\n", $2,$1}' > dates.txt
}

add-zero-days() {
    C=1
    while read -r l; do
        D_LINE="$(d_num "$(echo "$l" | cut -d ',' -f 1)")"

        while [[ "$D_LINE" -ge "$C" ]]; do
            if [[ "$D_LINE" -eq "$C" ]]; then
                echo "$l"
            else
                echo "$(y_num "$1" "$((C-1))"),0"
            fi
            
            C=$((C+1))
        done
    done < dates.txt
}

create-dates-year "$1"
add-zero-days "$1"
rm dates.txt
[[ "$RM_EXCLUDE_FILE" ]] && rm count-filter.txt
