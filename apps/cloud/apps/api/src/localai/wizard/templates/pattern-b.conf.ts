// The final vhost for a Pattern B node (Tailscale Serve HTTPS with a Tailscale-issued certificate), after
// docs/source/vps-ai-nodes-setup.md section 12.2. PLACEHOLDERS ONLY in this file: `{{name}}` is filled in by
// `snippets.ts` when a lab:admin opens the checklist, with the node's real tailnet address and name.
export const PATTERN_B_TEMPLATE = `# AI inference node {{nodeId}}, llama.cpp (Pattern B)
# Runbook: docs/runbooks/05-add-a-node.md
server {
    listen 80;
    listen [::]:80;

    server_name {{publicHost}};

    location /.well-known/acme-challenge/ {
        root /var/www/certbot;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;

    server_name {{publicHost}};

    ssl_certificate /etc/letsencrypt/live/{{publicHost}}/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/{{publicHost}}/privkey.pem;

    client_max_body_size 100m;

    location / {
        # The node over the private Tailscale network
        # (tailscale serve HTTPS :443 with a Tailscale-issued certificate).
        # The IP is used instead of the MagicDNS name so nginx never depends on MagicDNS at startup;
        # SNI and Host still carry the tailnet name.
        proxy_pass https://{{tailnetIp}}:443;

        proxy_ssl_server_name on;
        proxy_ssl_name {{tailnetName}};
        proxy_ssl_verify on;
        proxy_ssl_verify_depth 3;
        proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;

        proxy_http_version 1.1;

        # Tailscale Serve routes by hostname, so Host must be the tailnet name
        proxy_set_header Host {{tailnetName}};
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Authorization $http_authorization;

        # Stream LLM tokens immediately
        proxy_buffering off;
        proxy_cache off;
        proxy_set_header Connection "";

        # Long-running inference requests
        proxy_connect_timeout 15s;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;
    }
}
`;
