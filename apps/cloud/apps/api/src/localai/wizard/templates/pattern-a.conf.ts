// The final vhost for a Pattern A node (Tailscale Serve TCP, plain HTTP inside the tunnel), after
// docs/source/vps-ai-nodes-setup.md section 12.1. PLACEHOLDERS ONLY in this file: `{{name}}` is filled in by
// `snippets.ts` when a lab:admin opens the checklist, with the node's real tailnet address.
export const PATTERN_A_TEMPLATE = `# AI inference node {{nodeId}}, llama.cpp (Pattern A)
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
        # (tailscale serve --tcp=18080 -> 127.0.0.1:8080, plain HTTP inside the tailnet).
        # The IP, not the name, so nginx never depends on MagicDNS at startup.
        proxy_pass http://{{tailnetIp}}:18080;

        proxy_http_version 1.1;

        proxy_set_header Host $host;
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
