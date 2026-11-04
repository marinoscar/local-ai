// The bootstrap HTTP vhost (runbook 05, step 24): lets Let's Encrypt check the name before the certificate exists.
// PLACEHOLDERS ONLY in this file. `{{name}}` is filled in by `snippets.ts` when a lab:admin opens the checklist.
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
