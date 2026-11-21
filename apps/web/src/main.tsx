import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Authentication } from './Authentication';
import './styles.css';

createRoot(document.getElementById('root')!).render(<StrictMode><Authentication /></StrictMode>);
