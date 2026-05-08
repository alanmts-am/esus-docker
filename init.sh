#!/bin/sh
echo "Aguardando o banco de dados iniciar..."
until psql -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c '\q' > /dev/null 2>&1; do
  sleep 2
done
echo "Banco de dados pronto! Iniciando a instalação/configuração do e-SUS..."

java -jar pec.jar -console -url="jdbc:postgresql://$POSTGRES_HOST:$POSTGRES_PORT/$POSTGRES_DB" -username=$POSTGRES_USER -password=$POSTGRES_PASSWORD -continue

until psql -h "$POSTGRES_HOST" -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT 1 FROM tb_config_sistema LIMIT 1;" > /dev/null 2>&1; do
  sleep 5
done

psql -h $POSTGRES_HOST -p $POSTGRES_PORT -U $POSTGRES_USER -d $POSTGRES_DB -c "update tb_config_sistema set ds_texto = null, ds_inteiro = 1 where co_config_sistema = 'TREINAMENTO';"

unset POSTGRES_PASSWORD
 
exec "/opt/e-SUS/webserver/standalone.sh"
