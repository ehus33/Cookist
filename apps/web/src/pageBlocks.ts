export type BlockType = 'heading' | 'text' | 'image' | 'button' | 'divider' | 'spacer' | 'products';
export type PageBlock = {
  id: string;
  type: BlockType;
  text: string;
  url: string;
  alt: string;
  align: 'left' | 'center' | 'right';
  size: 'small' | 'medium' | 'large';
  beside?: boolean;
};

export const blockTypes: { type: BlockType; label: string }[] = [
  { type: 'heading', label: 'Heading' },
  { type: 'text', label: 'Text' },
  { type: 'image', label: 'Image' },
  { type: 'button', label: 'Button' },
  { type: 'divider', label: 'Divider' },
  { type: 'spacer', label: 'Space' },
  { type: 'products', label: 'Product catalog' },
];

export function newBlock(type: BlockType): PageBlock {
  return {
    id: crypto.randomUUID(), type, text: type === 'heading' ? 'Your next big idea' : type === 'text'
      ? 'Tell your customers what makes you different.' : type === 'button' ? 'Learn more' : '',
    url: '', alt: '', align: 'left', size: 'medium',
  };
}

export function isSafeWebURL(value: string): boolean {
  try {
    const url = new URL(value);
    return value === value.trim() && !/[\u0000-\u0020\u007f\\]/.test(value)
      && url.protocol === 'https:' && !!url.hostname && !url.username && !url.password;
  } catch {
    return false;
  }
}

export function blockError(blocks: PageBlock[]): string | null {
  if (blocks.length < 1 || blocks.length > 40) return 'Add between 1 and 40 elements to your page.';
  if (blocks.filter(block => block.type === 'products').length > 1) return 'Use only one product catalog per page.';
  for (const [index, block] of blocks.entries()) {
    if (block.beside && (index === 0 || blocks[index - 1].beside)) {
      return 'A row can contain at most two elements.';
    }
    if ((block.type === 'heading' || block.type === 'text' || block.type === 'button') && !block.text.trim()) {
      return `Element ${index + 1} needs text.`;
    }
    if ((block.type === 'button' || block.type === 'image') && !isSafeWebURL(block.url)) {
      return `Element ${index + 1} needs a complete HTTPS URL.`;
    }
    if (block.type === 'image' && !block.alt.trim()) return `Element ${index + 1} needs image alternative text.`;
  }
  return null;
}

export function moveBlock(blocks: PageBlock[], id: string, beforeIndex: number): PageBlock[] {
  const index = blocks.findIndex(block => block.id === id);
  if (index < 0 || beforeIndex < 0 || beforeIndex > blocks.length) return blocks;
  if (beforeIndex === index || beforeIndex === index + 1) return blocks;
  return insertBlock(removeBlock(blocks, id), blocks[index], beforeIndex > index ? beforeIndex - 1 : beforeIndex);
}

export function blockRows(blocks: PageBlock[]): PageBlock[][] {
  const rows: PageBlock[][] = [];
  for (const block of blocks) {
    if (block.beside && rows.length) rows[rows.length - 1].push(block);
    else rows.push([block]);
  }
  return rows;
}

export function removeBlock(blocks: PageBlock[], id: string): PageBlock[] {
  const index = blocks.findIndex(block => block.id === id);
  if (index < 0) return blocks;
  return blocks.filter(block => block.id !== id).map((block, nextIndex) =>
    nextIndex === index && block.beside ? { ...block, beside: false } : block);
}

export function insertBlock(blocks: PageBlock[], block: PageBlock, index: number): PageBlock[] {
  const next = blocks.map((item, position) => position === index && item.beside ? { ...item, beside: false } : item);
  next.splice(index, 0, { ...block, beside: false });
  return next;
}

export function placeBeside(blocks: PageBlock[], block: PageBlock, targetId: string, side: 'left' | 'right'): PageBlock[] {
  if (block.id === targetId || !blocks.some(item => item.id === targetId)) return blocks;
  const next = [...removeBlock(blocks, block.id)];
  const index = next.findIndex(item => item.id === targetId);
  if (next[index].beside || next[index + 1]?.beside) return blocks;
  next.splice(index, 1, ...(side === 'left'
    ? [{ ...block, beside: false }, { ...next[index], beside: true }]
    : [next[index], { ...block, beside: true }]));
  return next;
}

export function splitRow(blocks: PageBlock[], id: string): PageBlock[] {
  const index = blocks.findIndex(block => block.id === id);
  const secondIndex = blocks[index]?.beside ? index : index + 1;
  if (index < 0 || !blocks[secondIndex]?.beside) return blocks;
  return blocks.map((block, position) => position === secondIndex ? { ...block, beside: false } : block);
}
