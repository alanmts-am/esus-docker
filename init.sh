#!/bin/sh

java -jar pec.jar -console -url="jdbc:postgresql://$POSTGRES_HOST:$POSTGRES_PORT/$POSTGRES_DB" -username=$POSTGRES_USER -password=$POSTGRES_PASSWORD -continue

unset PGPASSWORD
 
exec "/opt/e-SUS/webserver/standalone.sh"
