import test from 'node:test';
import assert from 'node:assert/strict';
import { addArgs, updateArgs } from '../src/skills.js';

test('uses documented skills add flags', () => {
  assert.deepEqual(addArgs({ agent: 'codex', global: true }), [
    'skills', 'add', 'markthebault/tabucom', '--skill', 'tabucom', '--yes', '--agent', 'codex', '--global',
  ]);
});

test('uses the supported scoped Tabucom update command', () => {
  assert.deepEqual(updateArgs({ global: true }), ['skills', 'update', 'tabucom', '--yes', '--global']);
});
