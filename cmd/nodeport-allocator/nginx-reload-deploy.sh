#!/bin/bash
set +x  # trace
set -e  # exit immediately

if [[ $# -ne 1 ]]; then
    echo "Need parameters: DEPLOY_NAME"
    exit 1
fi

DEPLOY_NAME=$1
MASTER_URL="http://master-test.friday.yy.com"
DEPLOY_PATH="apis/extensions/v1beta1/namespaces/default/deployments"
DEPLOY_URL="$MASTER_URL/$DEPLOY_PATH"
DEPLOY_IDQ=".metadata.labels.app"

DEPLOY_ID=$(curl -sS "$DEPLOY_URL/$DEPLOY_NAME" | jq -r "$DEPLOY_IDQ")
if [[ -z $DEPLOY_ID || "$DEPLOY_ID" == "null" ]]; then
	echo "Can not get DEPLOY_ID from \"$DEPLOY_NAME\""
	exit 1
fi
#echo "Deployment \"$DEPLOY_NAME\"'s DEPLOY_ID is \"$DEPLOY_ID\""

POD_PATH="api/v1/namespaces/default/pods"
POD_URL="$MASTER_URL/$POD_PATH"
POD_SELECTOR="app%3D$DEPLOY_ID"

POD_LIST=$(curl -sS "$POD_URL?labelSelector=$POD_SELECTOR")
POD_NUM=$(echo "$POD_LIST" | jq -r ".items | length")
if [[ -z $POD_NUM || "$POD_NUM" == "0" ]]; then
	echo "No POD for \"$DEPLOY_NAME\""
	exit 1
fi

echo "upstream {"

for i in $(seq 0 $((POD_NUM-1))); do
	POD_NAME=$(echo "$POD_LIST" | jq -r ".items[$i].metadata.name")
	#echo $POD_NAME

	POD_PHASE=$(echo "$POD_LIST" | jq -r ".items[$i].status.phase")
	#echo $POD_PHASE
	if [[ "$POD_PHASE" != "Running" ]]; then
		continue
	fi

	POD_IP=$(echo "$POD_LIST" | jq -r ".items[$i].status.hostIP")
	#echo $POD_IP
	if [[ -z $POD_IP || "$POD_IP" == "null" ]]; then
		continue
	fi

	POD_PORT=$(curl -sS "http://$POD_IP:4195/getports?pod=$POD_NAME&num=0")
	PORT_NUM=$(echo "$POD_PORT" | jq -r ".Ports | length" 2>/dev/null)
	if [[ -z $PORT_NUM || "$PORT_NUM" == "0" ]]; then
		continue
	fi

	PORT0=$(echo "$POD_PORT" | jq -r ".Ports[0]")
	if [[ -z $PORT0 || "$PORT0" == "null" ]]; then
		continue
	fi

	echo "  $POD_IP:$PORT0"
done

echo "}"  # upstream }
