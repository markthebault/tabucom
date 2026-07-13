import test from 'node:test';
import assert from 'node:assert/strict';
import { normalizeBaseUrl } from '../src/validate.js';

test('accepts HTTPS and explicitly local HTTP origins', () => {
  assert.equal(normalizeBaseUrl('https://tabucom.example.com/'), 'https://tabucom.example.com');
  assert.equal(normalizeBaseUrl('http://localhost'), 'http://localhost');
  assert.equal(normalizeBaseUrl('http://localhost:8080'), 'http://localhost:8080');
  assert.equal(normalizeBaseUrl('http://127.0.0.1:8080'), 'http://127.0.0.1:8080');
  assert.equal(normalizeBaseUrl('http://[::1]:8080'), 'http://[::1]:8080');
});

for (const value of [
  'http://tabucom.example.com',
  'https://user:password@example.com',
  'https://example.com/path',
  'https://example.com?query=value',
  'https://example.com#fragment',
]) {
  test(`rejects ${value}`, () => assert.throws(() => normalizeBaseUrl(value)));
}
