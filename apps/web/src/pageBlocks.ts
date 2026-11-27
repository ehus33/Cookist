export type BlockType = 'heading' | 'text' | 'image' | 'button' | 'divider' | 'spacer' | 'products' | 'contact' | 'waitlist' | 'data-list' | 'data-form' | 'member-account';
export type PageBlock = {
  id: string;
  type: BlockType;
  text: string;
  url: string;
  alt: string;
  align: 'left' | 'center' | 'right';
  size: 'small' | 'medium' | 'large';
  beside?: boolean;
  collection?: string;
};

export const blockTypes: { type: BlockType; label: string }[] = [
  { type: 'heading', label: 'Heading' },
  { type: 'text', label: 'Text' },
  { type: 'image', label: 'Image' },
  { type: 'button', label: 'Button' },
  { type: 'divider', label: 'Divider' },
  { type: 'spacer', label: 'Space' },
  { type: 'products', label: 'Product catalog' },
  { type: 'contact', label: 'Contact form' },
  { type: 'waitlist', label: 'Waitlist form' },
  { type: 'member-account', label: 'Member account' },
  { type: 'data-list', label: 'Connected list' },
  { type: 'data-form', label: 'Connected form' },
];

export function newBlock(type: BlockType): PageBlock {
  return {
    id: crypto.randomUUID(), type, text: type === 'member-account' ? 'Member area' : type === 'data-list' ? 'Your records' : type === 'data-form' ? 'Create a record' : type === 'contact' ? 'Contact us' : type === 'waitlist' ? 'Join our waitlist' : type === 'heading' ? 'Your next big idea' : type === 'text'
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

export function blockError(blocks: PageBlock[], collections?: { id: string }[]): string | null {
  if (blocks.length < 1 || blocks.length > 40) return 'Add between 1 and 40 elements to your page.';
  if (blocks.filter(block => block.type === 'products').length > 1) return 'Use only one product catalog per page.';
  for (const [index, block] of blocks.entries()) {
    if (block.beside && (index === 0 || blocks[index - 1].beside)) {
      return 'A row can contain at most two elements.';
    }
    if (['heading', 'text', 'button', 'contact', 'waitlist', 'data-list', 'data-form', 'member-account'].includes(block.type) && !block.text.trim()) {
      return `Element ${index + 1} needs text.`;
    }
    if (['data-list', 'data-form', 'member-account'].includes(block.type) && !collections?.length) {
      return 'Enable dynamic data before publishing connected elements.';
    }
    if ((block.type === 'data-list' || block.type === 'data-form') && !collections?.some(item => item.id === block.collection)) {
      return `Element ${index + 1} needs a data collection.`;
    }

    if ((block.type === 'button' || block.type === 'image') && !isSafeWebURL(block.url)) {
      return `Element ${index + 1} needs a complete HTTPS URL.`;
    }
    if (block.type === 'image' && !block.alt.trim()) return `Element ${index + 1} needs image alternative text.`;
  }
  return null;
}

export function isPageBlock(value: unknown): value is PageBlock {
  if (!value || typeof value !== 'object') return false;
  const block = value as Record<string, unknown>;
  return typeof block.id === 'string' && /^[A-Za-z0-9_-]{1,64}$/.test(block.id)
    && blockTypes.some(item => item.type === block.type)
    && typeof block.text === 'string' && block.text.length <= 8000
    && typeof block.url === 'string' && block.url.length <= 2000
    && typeof block.alt === 'string' && block.alt.length <= 640
    && (block.align === 'left' || block.align === 'center' || block.align === 'right')
    && (block.size === 'small' || block.size === 'medium' || block.size === 'large')
    && (block.beside === undefined || typeof block.beside === 'boolean')
    && (block.collection === undefined || (typeof block.collection === 'string' && block.collection.length <= 32));
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
