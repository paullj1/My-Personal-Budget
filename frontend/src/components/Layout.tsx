import { PropsWithChildren, useEffect, useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';

import { clearToken, hasAuthToken } from '../api/client';

const Layout = ({ children }: PropsWithChildren) => {
  const location = useLocation();
  const navigate = useNavigate();
  const [authed, setAuthed] = useState(false);
  const [theme, setTheme] = useState<'system' | 'light' | 'dark'>('system');
  const [toolbarOpen, setToolbarOpen] = useState(false);

  useEffect(() => {
    const check = () => setAuthed(hasAuthToken());
    check();
    window.addEventListener('storage', check);
    return () => window.removeEventListener('storage', check);
  }, []);

  useEffect(() => {
    setAuthed(hasAuthToken());
  }, [location.pathname]);

  useEffect(() => {
    const saved = localStorage.getItem('mpb_theme');
    if (saved === 'light' || saved === 'dark' || saved === 'system') {
      setTheme(saved);
      applyTheme(saved);
    } else {
      applyTheme('system');
    }
  }, []);

  const applyTheme = (value: 'system' | 'light' | 'dark') => {
    const root = document.documentElement;
    let resolved = value;
    if (value === 'system') {
      resolved = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    }
    root.setAttribute('data-theme', resolved);
    localStorage.setItem('mpb_theme', value);
    setTheme(value);
  };

  const logout = () => {
    clearToken();
    setAuthed(false);
    navigate('/login');
  };

  return (
    <div className="page">
      <header className="topbar">
        <div className="brand">My Personal Budget</div>
        <nav>
          {!authed && (
            <>
              <Link to="/login" className={location.pathname === '/login' ? 'active' : undefined}>
                Login
              </Link>
              <Link to="/register" className={location.pathname === '/register' ? 'active' : undefined}>
                Register
              </Link>
            </>
          )}
        </nav>
        <button
          type="button"
          className="icon ghost"
          aria-expanded={toolbarOpen}
          aria-label={toolbarOpen ? 'Hide quick actions' : 'Show quick actions'}
          onClick={() => setToolbarOpen((v) => !v)}
        >
          {toolbarOpen ? '✖' : '☰'}
        </button>
      </header>
      {toolbarOpen && (
        <div className="toolbar">
          <div className="toolbar__section">
            <span className="eyebrow">Theme</span>
            <div className="toolbar__buttons">
              <button
                type="button"
                className={`ghost ${theme === 'system' ? 'active' : ''}`}
                aria-pressed={theme === 'system'}
                aria-label="System theme"
                onClick={() => applyTheme('system')}
              >
                🖥
              </button>
              <button
                type="button"
                className={`ghost ${theme === 'light' ? 'active' : ''}`}
                aria-pressed={theme === 'light'}
                aria-label="Light theme"
                onClick={() => applyTheme('light')}
              >
                ☀️
              </button>
              <button
                type="button"
                className={`ghost ${theme === 'dark' ? 'active' : ''}`}
                aria-pressed={theme === 'dark'}
                aria-label="Dark theme"
                onClick={() => applyTheme('dark')}
              >
                🌙
              </button>
            </div>
          </div>
          <div className="toolbar__section">
            <span className="eyebrow">Actions</span>
            <div className="toolbar__buttons">
              {authed && (
                <Link
                  to="/api-keys"
                  className={`ghost button-link ${location.pathname === '/api-keys' ? 'active' : ''}`}
                >
                  🔑 API keys
                </Link>
              )}
              {authed && (
                <button
                  type="button"
                  className="ghost"
                  aria-label="Create a budget"
                  onClick={() => window.dispatchEvent(new CustomEvent('open-new-budget'))}
                >
                  ➕ New budget
                </button>
              )}
              {authed && (
                <button type="button" className="ghost" onClick={logout} aria-label="Logout">
                  🚪 Logout
                </button>
              )}
            </div>
          </div>
        </div>
      )}
      <main className="content">{children}</main>
    </div>
  );
};

export default Layout;
