# 🚆 Trains Gare de Lyon — rue Jorge Semprún (POC)

Timeline temps réel des trains de **Paris Gare de Lyon**, triée par heure de passage la plus proche
de maintenant, pour que les riverains de la rue Jorge Semprún sachent quand ouvrir ou fermer leurs fenêtres.
Chaque locataire a sa propre instance et y marque les trains qu'il trouve bruyants.

**Statique + nginx** : pas de serveur applicatif ni de base de données. Seul un petit binaire Go
(6,6 Mo, bibliothèque standard) prépare les données ouvertes SNCF de secours.

## Lancer en local

```bash
scripts/dev.sh            # image du projet via Docker, http://localhost:8080
```

Sources de données, dans l'ordre :

| Badge | Source | Quand |
|---|---|---|
| **LIVE** | API SNCF (clé requise) : tous les trains en temps réel | clé installée et API joignable |
| **OPEN DATA** | Données ouvertes SNCF et Trenitalia (Frecciarossa), sans clé : horaires prévus + retards temps réel (surtout TGV/Intercités/Frecciarossa, une partie des RER D / R) ; les trains sans temps réel sont marqués « prévu » | API SNCF en erreur |
| **DÉMO** | Données simulées | ni l'un ni l'autre |

Forcer la démo : `http://localhost:8080/?demo`.

Avec une clé (gratuite : https://numerique.sncf.com/startup/api/) :

```bash
scripts/set-sncf-key.sh VOTRE_CLE      # écrit deploy/nginx/secrets/sncf_auth.inc (non versionné)
docker exec rails-semprun nginx -s reload
```

## Architecture

```
navigateur ──► nginx ──┬─ /                    fichiers statiques (public/)
                       ├─ /sncf/arrivals       relais api.sncf.com : ajoute la clé, cache 60 s
                       ├─ /sncf/departures     (seules ces 2 URL, uniquement Gare de Lyon)
                       ├─ /opendata.json       secours : préparé par opendata (Go, toutes les 2 min)
                       ├─ /declarer?…          écrit 1 ligne dans data/declarations.jsonl (journal nginx)
                       └─ /declarations.jsonl  lecture de mes trains bruyants
```

- `public/index.html` : interface.
- `public/data.js` : appels API SNCF, secours open data, démo, trains bruyants.
- `opendata/main.go` : télécharge les horaires théoriques SNCF, Transilien et Trenitalia France (GTFS, 1×/6 h) et le temps réel
  (GTFS-RT SNCF et Trenitalia, transport.data.gouv.fr), garde Gare de Lyon et écrit `data/opendata.json`. Cache dans `data/cache/`.
- `deploy/nginx/rails-semprun.conf` : configuration nginx (bloc `http`).
- `deploy/nginx/rails-sncf-proxy.inc` : réglages du relais SNCF.

**Quota SNCF** (5000 req/jour) : toutes les pages demandent exactement la même URL pour la journée,
nginx la garde en cache 60 s, donc environ 2 requêtes/min au maximum quel que soit le nombre d'appareils (~2900/jour).

## Mes trains bruyants

Une instance = un locataire. Toucher 🔊 sur un passage → `GET /declarer?train=<clé>&n=1&h=<HH:MM>&dir=<direction>`.
nginx l'écrit dans son journal d'accès au format JSON, une ligne par marquage :

```json
{"t":"1793616894.698","s":"204","train":"departure%7CTransilien%7CR%7C7039","n":"1","h":"12%3A02","dir":"Montereau"}
```

- `train` = `type|mode|ligne|numéro` (un même numéro de train repasse chaque jour à la même heure).
- `n` = 1 bruyant, 0 retrait ; seule la dernière ligne de chaque train compte.
- Le fichier fait foi : tous les appareils du locataire (téléphone, ordinateur) voient les mêmes marquages.
  Si le serveur est injoignable, la page garde une copie dans le navigateur et le signale.
- Requêtes invalides refusées (400, non écrites) ; POST interdit ; aucune IP ni navigateur enregistrés.

⚠️ Le fichier ne doit **pas** être pris en charge par logrotate. Sauvegarde : copier `data/declarations.jsonl`.

## Mise en production (Debian/Ubuntu)

```bash
sudo mkdir -p /var/www/rails/data /var/cache/nginx
sudo cp -r public /var/www/rails/
sudo chown www-data: /var/www/rails/data
sudo cp -r deploy/nginx/* /etc/nginx/conf.d/          # rails-semprun.conf, rails-sncf-proxy.inc, secrets/
sudo scripts/set-sncf-key.sh VOTRE_CLE /etc/nginx/conf.d/secrets/sncf_auth.inc
sudo nginx -t && sudo systemctl reload nginx
# secours open data : compiler le binaire Go (sans installer Go, via Docker)
docker run --rm -v "$PWD/opendata:/src" -w /src -e CGO_ENABLED=0 golang:1.25-alpine go build -ldflags="-s -w" -o opendata .
sudo install -m 755 opendata/opendata /usr/local/bin/opendata
sudo cp deploy/systemd/rails-opendata.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now rails-opendata
```

Puis ajouter `server_name` et HTTPS (certbot) dans `rails-semprun.conf`.

## Image Docker

```bash
DOCKER_USERNAME=… DOCKER_PASSWORD=… scripts/build.sh   # tag git du commit courant, sinon latest
```

Construit `melvinvmegen/nuisance-sonore-train` (modifiable via `IMAGE_NAME`), vérifie la config (`nginx -t`) puis pousse l'image ;
une version taguée pousse aussi `latest`. La clé SNCF n'est jamais dans l'image (`.dockerignore`), elle est montée au lancement :

```bash
docker run -d -p 80:80 \
  -v /etc/rails/sncf_auth.inc:/etc/nginx/conf.d/secrets/sncf_auth.inc:ro \
  -v rails-data:/var/www/rails/data \
  melvinvmegen/nuisance-sonore-train
```

Le volume `rails-data` conserve `declarations.jsonl` et le cache open data d'une version à l'autre.
Le script open data tourne dans le conteneur (désactivable avec `-e OPENDATA=0`).

**CI** (`.github/workflows/docker.yml`) : PR → build + `nginx -t` sans push (`NO_PUSH=1`) ; push sur `main` → `:latest` ;
tag git → `:<tag>` + `:latest`. Secrets du dépôt : `DOCKER_USERNAME`, `DOCKER_PASSWORD` (jeton d'accès Docker Hub).

## Fonctionnalités

- **Statut** : 🪟 calme, vous pouvez ouvrir / ⚠️ fermez bientôt / 🔇 train en passage, et prochaine accalmie.
- **Bande 60 min** et **vue journée** (une ligne par heure : passages, calme, nombre de trains).
- **Timeline** : 10 dernières minutes en grisé, puis les prochains passages, avec retards.
- **Trains bruyants 🔊** : marquage par passage, liste dans les réglages, option « ne compter que mes trains bruyants ».
- **Lignes prises en compte** : une case par ligne (RER D, TGV INOUI, Transilien R…) dans les réglages ; une ligne décochée disparaît du statut, des bandes et de la timeline.
- **Réglages** (dans le navigateur) : départs inclus (par défaut), décalage gare ↔ rue, durée du bruit,
  accalmie minimale, notification 2 min avant un passage.
- **Ma position** : adresse saisie (géocodée par la Géoplateforme IGN, `data.geopf.fr`, gratuit, sans clé)
  ou 📍 géolocalisation du navigateur (HTTPS requis, sauf `localhost`). Le décalage gare ↔ logement est alors
  calculé : distance à vol d'oiseau jusqu'aux quais de Gare de Lyon ÷ vitesse des trains (30 km/h par défaut, réglable),
  arrondi à 0,5 min. Au-delà de 8 km, retour au réglage manuel. Décochable pour régler à la main.

## Limites du POC

- Mode open data : la plupart des RER D / Transilien n'ont pas de temps réel (horaire prévu, ni retard ni suppression),
  et un train marqué 🔊 en open data peut ne pas être reconnu avec l'API SNCF (libellés de lignes différents).
- Frecciarossa (open data Trenitalia) : heures du GTFS en UTC (vérifié avec le temps réel), converties en heure de Paris ;
  numéro de train = `route_id` (6xxx Lyon/Marseille, 92xx Milan).

- L'heure de passage devant la rue est **estimée** depuis l'heure en gare (distance à vol d'oiseau ÷ vitesse, ou 2 min par défaut). À calibrer.
- La gare de référence reste Paris Gare de Lyon, quelle que soit la position.
- Les manœuvres et rames vides (voies de garage à ~30 m de la rue) ne sont pas dans l'API voyageurs.
- Les numéros de RER D / Transilien peuvent changer selon les jours : un signalement peut ne pas suivre.
- L'instance n'est pas protégée : quiconque connaît l'URL peut modifier les marquages. Ajouter un `auth_basic` nginx si elle est exposée sur Internet.
