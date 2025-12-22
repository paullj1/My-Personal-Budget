import { useEffect, useState } from 'react';
import { Route, Routes, Navigate, useLocation, useNavigate } from 'react-router-dom';

import Layout from './components/Layout';
import Dashboard from './pages/Dashboard';
import Login from './pages/Login';
import Register from './pages/Register';
import { hasAuthToken } from './api/client';

const App = () => {
  const location = useLocation();
  const navigate = useNavigate();
  const [authed, setAuthed] = useState(() => hasAuthToken());

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
    const onUnauthorized = () => {
      setAuthed(false);
      navigate('/login', { replace: true });
    };
    window.addEventListener('mpb-unauthorized', onUnauthorized);
    return () => window.removeEventListener('mpb-unauthorized', onUnauthorized);
  }, [navigate]);

  const requireAuth = (element: JSX.Element) => (authed ? element : <Navigate to="/login" replace />);

  return (
    <Layout>
      <Routes>
        <Route path="/" element={<Navigate to={authed ? '/dashboard' : '/login'} replace />} />
        <Route path="/dashboard" element={requireAuth(<Dashboard />)} />
        <Route path="/login" element={authed ? <Navigate to="/dashboard" replace /> : <Login />} />
        <Route path="/register" element={authed ? <Navigate to="/dashboard" replace /> : <Register />} />
        <Route path="*" element={<div className="card">Not found</div>} />
      </Routes>
    </Layout>
  );
};

export default App;
