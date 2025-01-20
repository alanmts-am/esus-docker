#!/bin/sh

psql -h $POSTGRES_HOST -p $POSTGRES_PORT -U $POSTGRES_USER -d $POSTGRES_DB -c "update tb_config_sistema set ds_texto = null, ds_inteiro = 1 where co_config_sistema = 'TREINAMENTO';"

java -jar pec.jar -console -url="jdbc:postgresql://$POSTGRES_HOST:$POSTGRES_PORT/$POSTGRES_DB" -username=$POSTGRES_USER -password=$POSTGRES_PASSWORD -continue

unset PGPASSWORD
 
exec "/opt/e-SUS/webserver/standalone.sh"
