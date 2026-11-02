# Réglages communs du relais vers api.sncf.com (inclus dans les locations /sncf/*)
proxy_ssl_server_name on;
proxy_http_version 1.1;
proxy_cache rails_sncf;
proxy_cache_valid 200 60s;
proxy_cache_lock on;
proxy_cache_use_stale error timeout updating http_500 http_502 http_503 http_504;
proxy_ignore_headers Cache-Control Expires Set-Cookie;
proxy_hide_header Set-Cookie;
proxy_hide_header WWW-Authenticate; # sinon le navigateur ouvre une popup de connexion sur un 401
proxy_connect_timeout 5s;
proxy_read_timeout 20s;
add_header X-Cache $upstream_cache_status;
