# Headers for proxying to backd. backd trusts X-Forwarded-For from nginx
# (TRUSTED_PROXIES), so login throttling sees each client's address.
proxy_http_version 1.1;
proxy_set_header Host $http_host; # with the port, so backd can tell its own origin (session cookies)
proxy_set_header X-Forwarded-For $remote_addr;
proxy_set_header X-Forwarded-Proto $scheme;
