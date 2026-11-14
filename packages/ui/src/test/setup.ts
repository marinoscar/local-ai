import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

// Unmount what each test rendered (vitest runs without globals, so RTL cannot register this itself).
afterEach(() => cleanup());
