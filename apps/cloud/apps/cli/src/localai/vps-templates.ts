// The vhost files the stack-agent writes (docs/specs/cloud-app.md section 4.10; runbook 05 steps 24 and 26): the same
// three templates the wizard's checklist shows (apps/api/src/localai/wizard/templates/), as a copy because the CLI
// cannot import the API. `vps-templates.test.ts` renders them with the wizard's placeholders and compares them with
// the API's golden files, so the two can never drift apart.
//
// PLACEHOLDERS ONLY in this file. `{{name}}` is filled in at publish time, from values the executor has validated.

export const BOOTSTRAP_HTTP_TEMPLATE = `server {
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
`;

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

export interface VhostValues {
  nodeId: string;
  publicHost: string;
  tailnetIp: string;
  tailnetName: string;
}

/** Fills `{{name}}`. A name with no value is a bug in the caller. */
export function renderTemplate(template: string, values: Readonly<Record<string, string>>): string {
  return template.replace(/\{\{([A-Za-z0-9]+)\}\}/g, (_match, name: string) => {
    const value = values[name];
    if (value === undefined) throw new Error(`template variable ${name} has no value`);
    return value;
  });
}

export const renderBootstrapVhost = (values: VhostValues): string => renderTemplate(BOOTSTRAP_HTTP_TEMPLATE, { ...values });

export const renderFinalVhost = (pattern: 'A' | 'B', values: VhostValues): string =>
  renderTemplate(pattern === 'A' ? PATTERN_A_TEMPLATE : PATTERN_B_TEMPLATE, { ...values });
