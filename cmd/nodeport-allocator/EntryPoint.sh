#!/bin/sh
set -x  # trace
set +e  # exit immediately

if [[ -z $PODNAME ]]; then
    echo "Error no variable: PODNAME"
    exit 1
fi

[[ -z $PORTNUM ]] && PORTNUM="1"

PORTURL="http://127.0.0.1:4195"

GETPORTS=$(curl -sS "$PORTURL/getports?pod=$PODNAME&num=$PORTNUM")
for i in $(seq 0 $((PORTNUM-1))); do
    PORT=$(echo $GETPORTS | jq -r ".Ports[$i]")
    echo "PORT$i=$PORT"
    sed -i "s/##PORT$i##/$PORT/g" conf/server.xml
done

function stop_hook {
    curl -sS "$PORTURL/deleteports?pod=$PODNAME&num=$PORTNUM"
}

function signal_handler {
    #kill -TERM $PID
    catalina.sh stop
    wait $PID
    stop_hook
}

trap signal_handler SIGHUP SIGINT SIGTERM SIGKILL SIGPIPE SIGALRM SIGUSR1 SIGUSR2
#./test-signal &
catalina.sh run &
PID=$!
wait $PID
stop_hook

# Test cases:
#   Run container foreground,
#     1. Press Ctrl+C
#     2. docker stop <Container_ID>
#     3. kill <pid of parent (sh EntryPoint.sh)>
#     4. kill <pid of child (C++ or Java process)>
#  In all cases,
#     1. stop_hook is always executed
#     2. signal_handler is executed in case 1, 2, and 3

# Test with:
# docker run -it --rm --net=host --pid=host -v /tmp:/tmp -e PODNAME=my-tomcat -e PORTNUM=3 101.226.20.190:5000/my-java-tomcat:v0.0.5
