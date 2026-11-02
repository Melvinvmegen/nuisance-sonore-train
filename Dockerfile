# Image de production : nginx officiel + fichiers statiques + config
# + opendata (binaire Go statique : secours sans clé API, tourne en tâche de fond dans le conteneur).
# La clé API SNCF n'est PAS dans l'image : monter le fichier au lancement (sinon open data, puis démo) :
#   docker run -d -p 80:80 \
#     -v /chemin/sncf_auth.inc:/etc/nginx/conf.d/secrets/sncf_auth.inc:ro \
#     -v rails-data:/var/www/rails/data \
#     melvinvmegen/nuisance-sonore-train

# ---------- Compilation du binaire open data (bibliothèque standard Go uniquement) ----------
FROM golang:1.25-alpine AS opendata
WORKDIR /src
COPY opendata/ .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /opendata .

# ---------- Image finale ----------
FROM nginx:1.27-alpine

# default.conf écoute aussi sur le port 80 : on le remplace par notre config
RUN rm -f /etc/nginx/conf.d/default.conf \
 && mkdir -p /etc/nginx/conf.d/secrets /var/www/rails/data \
 && chown nginx:nginx /var/www/rails/data

COPY deploy/nginx/rails-semprun.conf deploy/nginx/rails-sncf-proxy.inc /etc/nginx/conf.d/
COPY --from=opendata /opendata /usr/local/bin/opendata
COPY --chmod=755 deploy/docker/40-opendata.sh /docker-entrypoint.d/40-opendata.sh
COPY public/ /var/www/rails/public/

# Journal des trains bruyants + cache open data : à conserver entre deux versions
VOLUME /var/www/rails/data
EXPOSE 80
