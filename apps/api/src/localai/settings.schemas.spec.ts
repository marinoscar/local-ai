import { BadRequestException, ForbiddenException } from '@nestjs/common';

import { checkSettingsBody, visibleSettings } from './settings.schemas';

const LAB = ['lab:read', 'lab:admin'];
const GW = ['lab:read', 'gateway:admin'];
const BOTH = ['lab:read', 'lab:admin', 'gateway:admin'];

describe('checkSettingsBody: the permission per key prefix', () => {
  it('a gateway key: gateway:admin passes, lab:admin alone is 403', () => {
    expect(checkSettingsBody({ 'gateway.maxUserKeyDays': 30 }, GW)).toEqual({ 'gateway.maxUserKeyDays': 30 });
    expect(() => checkSettingsBody({ 'gateway.maxUserKeyDays': 30 }, LAB)).toThrow(ForbiddenException);
  });

  it('an alerts key: lab:admin passes, gateway:admin alone is 403', () => {
    expect(checkSettingsBody({ 'alerts.repeatHours': 12 }, LAB)).toEqual({ 'alerts.repeatHours': 12 });
    expect(() => checkSettingsBody({ 'alerts.repeatHours': 12 }, GW)).toThrow(ForbiddenException);
  });

  it('every other prefix is lab:admin (catalog, agent, vps, checklist)', () => {
    for (const body of [{ 'catalog.githubRepo': 'org/repo' }, { 'agent.latestVersion': '1.2.3' }, { 'vps.stagingSuffix': '-staging' }, { 'checklist.vpsPublicHostname': 'ai.example.test' }]) {
      expect(() => checkSettingsBody(body, GW)).toThrow(ForbiddenException);
      expect(() => checkSettingsBody(body, LAB)).not.toThrow();
    }
  });

  it('a body mixing prefixes needs both permissions', () => {
    const body = { 'alerts.repeatHours': 12, 'gateway.maxAttempts': 2 };
    expect(() => checkSettingsBody(body, LAB)).toThrow(ForbiddenException);
    expect(() => checkSettingsBody(body, GW)).toThrow(ForbiddenException);
    expect(checkSettingsBody(body, BOTH)).toEqual(body);
  });

  it('a caller with neither permission is 403', () => {
    expect(() => checkSettingsBody({ 'alerts.repeatHours': 12 }, ['lab:read'])).toThrow(ForbiddenException);
  });
});

describe('checkSettingsBody: the body', () => {
  it.each([null, undefined, [], 'x', 3, {}])('refuses %j (400)', (body) => {
    expect(() => checkSettingsBody(body, BOTH)).toThrow(BadRequestException);
  });

  it('refuses an unknown key before it checks permissions (400)', () => {
    expect(() => checkSettingsBody({ 'gateway.nope': 1 }, ['lab:read'])).toThrow(BadRequestException);
    expect(() => checkSettingsBody({ 'alerts': { repeatHours: 1 } }, BOTH)).toThrow(BadRequestException);
  });

  it('validates every value with the key schema and names the key (400)', () => {
    let error: BadRequestException | undefined;
    try {
      checkSettingsBody({ 'alerts.repeatHours': 0, 'gateway.defaults.user': { rpm: 1 }, 'gateway.corsOrigins': ['*'], 'gateway.maxAttempts': 3 }, BOTH);
    } catch (caught) {
      error = caught as BadRequestException;
    }
    const paths = ((error!.getResponse() as { details: { issues: Array<{ path: string }> } }).details.issues).map((issue) => issue.path.split('.')[0] + '.' + issue.path.split('.')[1]);
    expect(error).toBeInstanceOf(BadRequestException);
    expect(paths.join(' ')).toContain('alerts.repeatHours');
    expect(paths.join(' ')).toContain('gateway.defaults');
    expect(paths.join(' ')).toContain('gateway.corsOrigins');
    expect(paths.join(' ')).not.toContain('gateway.maxAttempts');
  });

  it('accepts a null where the key allows it', () => {
    expect(checkSettingsBody({ 'agent.latestVersion': null, 'gateway.tailnetSuffix': null, 'gateway.defaults.service': { rpm: 1, concurrent: 1, dailyTokens: null, expiresInDays: null } }, BOTH)).toBeDefined();
  });

  it('refuses a null on a key that must have a value, and a wildcard origin', () => {
    expect(() => checkSettingsBody({ 'gateway.maxUserKeyDays': null }, BOTH)).toThrow(BadRequestException);
    expect(() => checkSettingsBody({ 'gateway.corsOrigins': ['*'] }, BOTH)).toThrow(BadRequestException);
    expect(checkSettingsBody({ 'gateway.corsOrigins': ['https://chat.example.test', 'http://localhost:3000'] }, BOTH)).toBeDefined();
  });
});

describe('visibleSettings', () => {
  const flat = { 'alerts.repeatHours': 24, 'gateway.tailnetSuffix': 'mynet.ts.net' };
  it('hides the tailnet name from a reader who is not an administrator', () => {
    expect(visibleSettings(flat, ['lab:read'])).toEqual({ 'alerts.repeatHours': 24 });
    expect(visibleSettings(flat, ['lab:read', 'gateway:use'])).not.toHaveProperty('gateway.tailnetSuffix');
  });
  it('shows it to either administrator', () => {
    expect(visibleSettings(flat, LAB)).toEqual(flat);
    expect(visibleSettings(flat, GW)).toEqual(flat);
  });
});
