#!/bin/sh
set +x  # trace
set -e  # exit immediately

if [[ -z $PODNAME ]]; then
    echo "Error no variable: PODNAME"
    exit 1
fi

[[ -z $PORTNUM ]] && PORTNUM="1"

PORTURL="http://127.0.0.1:4195"

function clean_up {
    curl -sS "$PORTURL/deleteports?pod=$PODNAME&num=$PORTNUM"
    exit
}

trap clean_up SIGHUP SIGINT SIGTERM SIGKILL SIGPIPE SIGALRM SIGUSR1 SIGUSR2

GETPORTS=$(curl -sS "$PORTURL/getports?pod=$PODNAME&num=$PORTNUM")
for i in $(seq 0 $((PORTNUM-1))); do
    PORT=$(echo $GETPORTS | jq -r ".Ports[$i]")
    echo "PORT$i=$PORT"
    sed -i "s/##PORT$i##/$PORT/g" conf/server.xml
done

catalina.sh run
clean_up
