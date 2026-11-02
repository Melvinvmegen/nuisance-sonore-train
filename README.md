# 🚆 Trains Gare de Lyon — rue Jorge Semprún (POC)

Timeline temps réel des trains de **Paris Gare de Lyon**, triée par heure de passage la plus proche
de maintenant, pour que les riverains de la rue Jorge Semprún sachent quand ouvrir ou fermer leurs fenêtres.
Chaque locataire a sa propre instance et y marque les trains qu'il trouve bruyants.

**100 % statique + nginx** : pas de serveur applicatif ni de base de données.

## Lancer en local

```bash
scripts/dev.sh            # nginx officiel via Docker, http://localhost:8080
```

Sans clé API SNCF, l'app passe automatiquement en **mode démo** (données simulées).
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
                       ├─ /declarer?…          écrit 1 ligne dans data/declarations.jsonl (journal nginx)
                       └─ /declarations.jsonl  lecture de mes trains bruyants
```

- `public/index.html` : interface.
- `public/data.js` : appels API SNCF, conversion des horaires, démo, trains bruyants.
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
```

Puis ajouter `server_name` et HTTPS (certbot) dans `rails-semprun.conf`.

## Fonctionnalités

- **Statut** : 🪟 calme, vous pouvez ouvrir / ⚠️ fermez bientôt / 🔇 train en passage, et prochaine accalmie.
- **Bande 60 min** et **vue journée** (une ligne par heure : passages, calme, nombre de trains).
- **Timeline** : 10 dernières minutes en grisé, puis les prochains passages, avec retards.
- **Trains bruyants 🔊** : marquage par passage, liste dans les réglages, option « ne compter que mes trains bruyants ».
- **Réglages** (dans le navigateur) : départs inclus (par défaut), décalage gare ↔ rue, durée du bruit,
  accalmie minimale, notification 2 min avant un passage.

## Limites du POC

- L'heure de passage devant la rue est **estimée** depuis l'heure en gare (2 min avant une arrivée, 2 min après un départ). À calibrer.
- Les manœuvres et rames vides (voies de garage à ~30 m de la rue) ne sont pas dans l'API voyageurs.
- Les numéros de RER D / Transilien peuvent changer selon les jours : un signalement peut ne pas suivre.
- L'instance n'est pas protégée : quiconque connaît l'URL peut modifier les marquages. Ajouter un `auth_basic` nginx si elle est exposée sur Internet.
