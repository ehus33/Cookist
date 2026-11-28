import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Authentication } from './features/auth/Authentication';
import './styles.css';

createRoot(document.getElementById('root')!).render(<StrictMode><Authentication /></StrictMode>);
