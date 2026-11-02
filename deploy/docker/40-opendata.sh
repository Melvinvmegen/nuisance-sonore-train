#!/bin/sh
# Lancé par l'entrypoint de l'image nginx avant nginx : prépare l'open data SNCF (secours sans clé API)
# en tâche de fond (utilisateur nginx), recalculé toutes les 2 min. OPENDATA=0 pour le désactiver.
[ "${OPENDATA:-1}" = "0" ] && exit 0
su nginx -s /bin/sh -c "exec /usr/local/bin/opendata --out /var/www/rails/data/opendata.json --loop ${OPENDATA_INTERVAL:-120}" &
