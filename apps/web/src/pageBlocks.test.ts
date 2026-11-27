import { expect, it } from 'vitest';
import { blockError, blockRows, blockTypes, insertBlock, moveBlock, newBlock, placeBeside, removeBlock, splitRow } from './pageBlocks';
import type { PageBlock } from './pageBlocks';

function element(id: string, beside = false): PageBlock {
  return { ...newBlock('text'), id, text: id, beside };
}

it('pairs any mix of element types in left/right order without making a third column', () => {
  for (const left of blockTypes) {
    for (const right of blockTypes) {
      const first = { ...newBlock(left.type), id: 'first' };
      const second = { ...newBlock(right.type), id: 'second' };
      const original = [first];
      const paired = placeBeside(original, second, first.id, 'right');
      expect(original).toEqual([first]);
      expect(blockRows(paired).map(row => row.map(block => block.id))).toEqual([['first', 'second']]);
      expect(blockRows(placeBeside([first], second, first.id, 'left')).map(row => row.map(block => block.id))).toEqual([['second', 'first']]);
      expect(placeBeside(paired, element('third'), first.id, 'right')).toBe(paired);
      expect(placeBeside(paired, element('third'), second.id, 'left')).toBe(paired);
    }
  }
});

it('preserves unrelated pairs while removing, moving, splitting and inserting rows', () => {
  const original = [element('a'), element('b', true), element('c'), element('d', true), element('e')];
  const rows = (blocks: PageBlock[]) => blockRows(blocks).map(row => row.map(block => block.id));
  expect(rows(removeBlock(original, 'a'))).toEqual([['b'], ['c', 'd'], ['e']]);
  expect(rows(removeBlock(original, 'b'))).toEqual([['a'], ['c', 'd'], ['e']]);
  expect(rows(moveBlock(original, 'a', 5))).toEqual([['b'], ['c', 'd'], ['e'], ['a']]);
  expect(rows(moveBlock(original, 'b', 5))).toEqual([['a'], ['c', 'd'], ['e'], ['b']]);
  expect(rows(insertBlock(original, element('new'), 1))).toEqual([['a'], ['new'], ['b'], ['c', 'd'], ['e']]);
  expect(rows(splitRow(original, 'a'))).toEqual([['a'], ['b'], ['c', 'd'], ['e']]);
  expect(rows(splitRow(original, 'b'))).toEqual([['a'], ['b'], ['c', 'd'], ['e']]);
  expect(rows(placeBeside(original, original[1], 'e', 'left'))).toEqual([['a'], ['c', 'd'], ['b', 'e']]);
  expect(rows(placeBeside(original, original[1], 'a', 'left'))).toEqual([['b', 'a'], ['c', 'd'], ['e']]);
  expect(rows(original)).toEqual([['a', 'b'], ['c', 'd'], ['e']]);
});

it('rejects malformed rows but accepts omitted flags from the original builder', () => {
  expect(blockError([element('a', true)])).toContain('at most two');
  expect(blockError([element('a'), element('b', true), element('c', true)])).toContain('at most two');
  expect(blockError([{ ...element('a'), beside: undefined }, element('b')])).toBeNull();
  expect(blockError([element('a'), element('b', true), element('c'), element('d', true)])).toBeNull();
});
