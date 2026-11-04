import { APP_NAME, THEME_COLOR } from '@app/shared';

import { readIdentity, renderIndexHtml, renderManifest } from '../../vite.config';

// The page shell and the manifest come from identity.json, so a rename edits
// one file and the browser tab, the install prompt and the theme follow.
describe('the identity in the page shell', () => {
  it('renders the product name into <title> and the brand colour into theme-color', () => {
    const html = renderIndexHtml('<meta name="theme-color" content="%APP_THEME_COLOR%" /><title>%APP_NAME%</title>', readIdentity());
    expect(html).toContain(`<title>${APP_NAME}</title>`);
    expect(html).toContain(`content="${THEME_COLOR}"`);
  });

  it('names the app in the manifest', () => {
    const manifest = JSON.parse(renderManifest(readIdentity())) as { name: string; theme_color: string };
    expect(manifest.name).toBe(APP_NAME);
    expect(manifest.theme_color).toBe(THEME_COLOR);
  });
});
