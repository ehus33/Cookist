import { useState } from 'react';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, expect, it } from 'vitest';
import { SiteEditor } from './SiteEditor';
import type { PageBlock } from './pageBlocks';

afterEach(cleanup);

function Editor() {
  const [blocks, setBlocks] = useState<PageBlock[]>([]);
  return <SiteEditor blocks={blocks} onChange={setBlocks} style="modern" disabled={false} catalog={<p>Seller catalog</p>} />;
}

it('adds, selects, edits, reorders, duplicates and deletes elements with undo and redo', () => {
  render(<Editor />);
  fireEvent.click(screen.getByRole('button', { name: 'Add Heading' }));
  fireEvent.change(screen.getByLabelText('Element text'), { target: { value: '<Welcome>' } });
  fireEvent.click(screen.getByRole('button', { name: 'Add Text' }));
  fireEvent.change(screen.getByLabelText('Element text'), { target: { value: 'Our story' } });
  const canvas = screen.getByRole('region', { name: 'Page canvas' });
  expect(canvas.querySelector('script')).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: 'Move element 2 up' }));
  expect(canvas.textContent!.indexOf('Our story')).toBeLessThan(canvas.textContent!.indexOf('<Welcome>'));
  fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
  expect(canvas.textContent!.indexOf('<Welcome>')).toBeLessThan(canvas.textContent!.indexOf('Our story'));
  fireEvent.click(screen.getByRole('button', { name: 'Redo' }));
  fireEvent.click(screen.getByRole('button', { name: 'Duplicate element' }));
  expect(within(canvas).getAllByText('Our story')).toHaveLength(2);
  fireEvent.click(screen.getByRole('button', { name: 'Delete element' }));
  expect(within(canvas).getAllByText('Our story')).toHaveLength(1);
  fireEvent.click(screen.getByRole('button', { name: 'Phone preview' }));
  expect(canvas).toHaveClass('phone-preview');
  fireEvent.click(screen.getByRole('button', { name: 'Preview page' }));
  expect(screen.queryByRole('button', { name: 'Edit element 1' })).not.toBeInTheDocument();
  expect(within(canvas).getByText('<Welcome>')).toBeInTheDocument();
});

it('accepts palette and reorder drags but ignores foreign drops and duplicate catalogs', () => {
  render(<Editor />);
  const transfer = { setData: () => undefined, effectAllowed: 'copy' };
  fireEvent.dragStart(screen.getByRole('button', { name: 'Add Text' }), { dataTransfer: transfer });
  fireEvent.drop(screen.getByRole('region', { name: 'Drop element at position 1' }), { dataTransfer: transfer });
  fireEvent.click(screen.getByRole('button', { name: 'Add Heading' }));
  fireEvent.dragStart(screen.getByRole('button', { name: 'Drag element 2' }), { dataTransfer: transfer });
  fireEvent.drop(screen.getByRole('region', { name: 'Drop element at position 1' }), { dataTransfer: transfer });
  const canvas = screen.getByRole('region', { name: 'Page canvas' });
  expect(canvas.textContent!.indexOf('Your next big idea')).toBeLessThan(canvas.textContent!.indexOf('Tell your customers'));
  fireEvent.drop(screen.getByRole('region', { name: 'Drop element at position 1' }), {
    dataTransfer: { getData: () => '{"type":"script"}' },
  });
  expect(screen.getByText('2/40 elements')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Add Product catalog' }));
  expect(within(canvas).getByText('Seller catalog')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Add Product catalog' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Duplicate element' })).toBeDisabled();
});

it('does not execute preview links or render unsafe image URLs', () => {
  render(<Editor />);
  fireEvent.click(screen.getByRole('button', { name: 'Add Image' }));
  fireEvent.change(screen.getByLabelText('Image URL'), { target: { value: 'javascript:alert(1)' } });
  const canvas = screen.getByRole('region', { name: 'Page canvas' });
  expect(canvas.querySelector('img')).toBeNull();
  fireEvent.change(screen.getByLabelText('Image URL'), { target: { value: 'https://example.com/image.png' } });
  fireEvent.change(screen.getByLabelText('Image alternative text'), { target: { value: 'Our shop' } });
  expect(within(canvas).getByRole('img', { name: 'Our shop' })).toHaveAttribute('referrerpolicy', 'no-referrer');
  fireEvent.click(screen.getByRole('button', { name: 'Add Button' }));
  fireEvent.change(screen.getByLabelText('Button URL'), { target: { value: 'https://example.com' } });
  fireEvent.click(screen.getByRole('button', { name: 'Preview page' }));
  expect(canvas.querySelector('a')).toBeNull();
});

it('drops mixed elements left and right, limits pairs, and can undo, split or delete paired elements', () => {
  render(<Editor />);
  const transfer = { setData: () => undefined, effectAllowed: 'copy' };
  fireEvent.click(screen.getByRole('button', { name: 'Add Heading' }));
  fireEvent.dragStart(screen.getByRole('button', { name: 'Add Text' }), { dataTransfer: transfer });
  fireEvent.drop(screen.getByRole('region', { name: 'Drop left of element 1' }), { dataTransfer: transfer });
  const canvas = screen.getByRole('region', { name: 'Page canvas' });
  const row = within(canvas).getByRole('group', { name: 'Page row 1' });
  expect(row).toHaveClass('page-row-pair');
  expect(row.querySelectorAll('.page-block')[0]).toHaveTextContent('Tell your customers');
  expect(row.querySelectorAll('.page-block')[1]).toHaveTextContent('Your next big idea');
  expect(within(canvas).queryByRole('region', { name: /Drop (left|right) of/ })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
  expect(row).not.toBeInTheDocument();
  expect(within(canvas).getByRole('group', { name: 'Page row 1' })).not.toHaveClass('page-row-pair');
  fireEvent.click(screen.getByRole('button', { name: 'Redo' }));
  fireEvent.click(screen.getByRole('button', { name: 'Edit element 1' }));
  fireEvent.click(screen.getByRole('button', { name: 'Delete element' }));
  expect(within(canvas).getByRole('group', { name: 'Page row 1' })).not.toHaveClass('page-row-pair');
  fireEvent.dragStart(screen.getByRole('button', { name: 'Add Divider' }), { dataTransfer: transfer });
  fireEvent.drop(screen.getByRole('region', { name: 'Drop right of element 1' }), { dataTransfer: transfer });
  expect(within(canvas).getByRole('group', { name: 'Page row 1' })).toHaveClass('page-row-pair');
  fireEvent.click(screen.getByRole('button', { name: 'Stack row vertically' }));
  expect(canvas.querySelector('.page-row-pair')).toBeNull();
  expect(within(canvas).getAllByRole('group', { name: /Page row/ })).toHaveLength(2);
});

it('supports side insertion and pairing through keyboard-accessible controls', () => {
  render(<Editor />);
  fireEvent.click(screen.getByRole('button', { name: 'Add Heading' }));
  fireEvent.change(screen.getByLabelText('Add placement'), { target: { value: 'right' } });
  fireEvent.click(screen.getByRole('button', { name: 'Add Button' }));
  const canvas = screen.getByRole('region', { name: 'Page canvas' });
  expect(canvas.querySelector('.page-row-pair')?.querySelectorAll('.page-block')).toHaveLength(2);
  fireEvent.click(screen.getByRole('button', { name: 'Stack row vertically' }));
  fireEvent.click(screen.getByRole('button', { name: 'Place beside previous' }));
  expect(canvas.querySelector('.page-row-pair')?.querySelectorAll('.page-block')).toHaveLength(2);
  fireEvent.click(screen.getByRole('button', { name: 'Duplicate element' }));
  expect(canvas.querySelectorAll('.page-row')).toHaveLength(2);
  expect(canvas.querySelectorAll('.page-row-pair')).toHaveLength(1);
});
