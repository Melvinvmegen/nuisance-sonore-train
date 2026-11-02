# Image de production : nginx officiel + fichiers statiques + config.
# La clé API SNCF n'est PAS dans l'image : monter le fichier au lancement (sinon mode démo) :
#   docker run -d -p 80:80 \
#     -v /chemin/sncf_auth.inc:/etc/nginx/conf.d/secrets/sncf_auth.inc:ro \
#     -v rails-data:/var/www/rails/data \
#     melvinvmegen/nuisance-sonore-train
FROM nginx:1.27-alpine

# default.conf écoute aussi sur le port 80 : on le remplace par notre config
RUN rm -f /etc/nginx/conf.d/default.conf \
 && mkdir -p /etc/nginx/conf.d/secrets /var/www/rails/data \
 && chown nginx:nginx /var/www/rails/data

COPY deploy/nginx/rails-semprun.conf deploy/nginx/rails-sncf-proxy.inc /etc/nginx/conf.d/
COPY public/ /var/www/rails/public/

# Journal des trains bruyants (data/declarations.jsonl) : à conserver entre deux versions
VOLUME /var/www/rails/data
EXPOSE 80
