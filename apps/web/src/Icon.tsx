import type { CSSProperties } from 'react';

const paths = {
  search: <><circle cx="10.8" cy="10.8" r="6.8" /><path d="m16 16 4 4" /></>,
  bag: <><path d="M5 8h14l1 13H4L5 8Z" /><path d="M8 9V6a4 4 0 0 1 8 0v3" /></>,
  home: <><path d="m3 10 9-7 9 7v11H3V10Z" /><path d="M9 21v-8h6v8" /></>,
  pin: <><path d="M19 10c0 5-7 11-7 11S5 15 5 10a7 7 0 1 1 14 0Z" /><circle cx="12" cy="10" r="2" /></>,
  arrow: <><path d="M4 12h16m-6-6 6 6-6 6" /></>,
  back: <><path d="M20 12H4m6-6-6 6 6 6" /></>,
  clock: <><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>,
  star: <><path d="m12 3 2.8 5.7 6.2.9-4.5 4.4 1.1 6.2-5.6-3-5.6 3 1.1-6.2L3 9.6l6.2-.9L12 3Z" /></>,
  receipt: <><path d="M5 3h14v18l-3-2-4 2-4-2-3 2V3Z" /><path d="M9 7h6M9 11h6M9 15h3" /></>,
  close: <><path d="m6 6 12 12M6 18 18 6" /></>,
  plus: <><path d="M12 5v14M5 12h14" /></>,
  minus: <><path d="M5 12h14" /></>,
  check: <><path d="m5 12 4 4L19 6" /></>,
  chevron: <><path d="m7 10 5 5 5-5" /></>,
  leaf: <><path d="M20 3C10 2 3 6 4 13c1 9 17 9 16-10Z" /><path d="m5 19 10-10" /></>,
  shop: <><path d="M4 10v11h16V10M2 10l3-7h14l3 7H2ZM9 21v-7h6v7" /></>,
  bike: <><circle cx="5" cy="17" r="4" /><circle cx="19" cy="17" r="4" /><path d="m5 17 5-9 5 9h4l-3-12h-3M8 5h4" /></>,
  logout: <><path d="M9 4H4v16h5m5-13 5 5-5 5m-6-5h11" /></>,
  sliders: <><path d="M4 7h16M4 17h16" /><circle cx="9" cy="7" r="2" /><circle cx="15" cy="17" r="2" /></>,
};

export function Icon({ name, size = 20, className = '', style }: {
  name: keyof typeof paths; size?: number; className?: string; style?: CSSProperties;
}) {
  return <svg aria-hidden="true" width={size} height={size} viewBox="0 0 24 24"
    fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"
    className={className} style={style}>{paths[name]}</svg>;
}
