import { expect, it } from 'vitest';
import { blockError, blockRows } from './pageBlocks';
import { newStartupSection, startupSections } from './startupSections';

it('creates a centered hero and balanced feature and pricing rows using standard elements', () => {
  const hero = newStartupSection('hero');
  expect(hero.map(block => block.type)).toEqual(['heading', 'text', 'button']);
  expect(hero.every(block => block.align === 'center')).toBe(true);
  expect(blockRows(hero).map(row => row.length)).toEqual([1, 1, 1]);

  expect(blockRows(newStartupSection('features')).map(row => row.map(block => block.type)))
    .toEqual([['heading'], ['text'], ['heading', 'heading'], ['text', 'text']]);
  expect(blockRows(newStartupSection('pricing')).map(row => row.map(block => block.type)))
    .toEqual([['heading'], ['text'], ['heading', 'heading'], ['text', 'text'], ['button', 'button']]);
  expect(blockRows(newStartupSection('testimonials')).map(row => row.length)).toEqual([1, 1, 2, 2]);
  expect(blockRows(newStartupSection('faq')).map(row => row.map(block => block.type)))
    .toEqual([['heading'], ['text'], ['heading'], ['text'], ['heading'], ['text']]);
});

it('keeps every preset within the row limit and requires real button destinations before publishing', () => {
  for (const { id } of startupSections) {
    const blocks = newStartupSection(id);
    expect(blocks.length).toBeGreaterThan(0);
    expect(blocks.length).toBeLessThanOrEqual(12);
    expect(blocks.some(block => block.type === 'products' || block.type === 'image')).toBe(false);
    expect(blocks[0].beside).not.toBe(true);
    expect(blockRows(blocks).every(row => row.length <= 2)).toBe(true);
    expect(blocks.every(block => block.url === '')).toBe(true);
    expect(blocks.every(block => block.text.trim().length > 0)).toBe(true);
    if (blocks.some(block => block.type === 'button')) {
      expect(blockError(blocks)).toContain('needs a complete HTTPS URL');
    } else {
      expect(blockError(blocks)).toBeNull();
    }
  }
});

it('creates fresh IDs and independent content on every call without mutating earlier presets', () => {
  const ids = new Set<string>();
  for (const { id } of startupSections) {
    const first = newStartupSection(id);
    const second = newStartupSection(id);
    expect(second).not.toBe(first);
    expect(second.map(({ id: _id, ...content }) => content))
      .toEqual(first.map(({ id: _id, ...content }) => content));
    for (const block of [...first, ...second]) {
      expect(ids.has(block.id)).toBe(false);
      ids.add(block.id);
    }
    first[0].text = 'Changed heading';
    first[0].beside = true;
    first[0].url = 'https://changed.invalid';
    first.pop();
    expect(newStartupSection(id).map(({ id: _id, ...content }) => content))
      .toEqual(second.map(({ id: _id, ...content }) => content));
  }
});
