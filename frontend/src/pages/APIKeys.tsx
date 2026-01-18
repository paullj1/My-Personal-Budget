import { FormEvent, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { request } from '../api/client';

type APIKey = {
  id: number;
  user_id: number;
  email: string;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at?: string | null;
};

type APIKeysResponse = {
  data: APIKey[];
  meta: { count: number };
};

type CreateKeyResponse = APIKey & { token: string };

const formatDate = (value?: string | null) => {
  if (!value) return 'Never';
  const parsed = new Date(value);
  if (Number.isNaN(parsed.valueOf())) return 'Unknown';
  return parsed.toLocaleString();
};

const APIKeys = () => {
  const queryClient = useQueryClient();
  const [name, setName] = useState('');
  const [newToken, setNewToken] = useState<CreateKeyResponse | null>(null);
  const navigate = useNavigate();

  const keysQuery = useQuery({
    queryKey: ['api-keys'],
    queryFn: () => request<APIKeysResponse>('/api/v1/api-keys')
  });

  const createKey = useMutation({
    mutationFn: (payload: { name: string }) =>
      request<CreateKeyResponse>('/api/v1/api-keys', {
        method: 'POST',
        body: payload.name ? { name: payload.name } : {}
      }),
    onSuccess: (data) => {
      setNewToken(data);
      setName('');
      queryClient.invalidateQueries({ queryKey: ['api-keys'] });
    }
  });

  const deleteKey = useMutation({
    mutationFn: (id: number) => request(`/api/v1/api-keys/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['api-keys'] });
    }
  });

  const keys = keysQuery.data?.data ?? [];
  const ownerEmail = useMemo(() => keys[0]?.email ?? newToken?.email ?? '', [keys, newToken]);

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    createKey.mutate({ name: name.trim() });
  };

  return (
    <section className="card">
      <header className="card__header">
        <div>
          <p className="eyebrow">Integrations</p>
          <h1>API Keys</h1>
          <p className="muted">
            Use these keys to authenticate MCP requests at <code>/mcp</code> with{' '}
            <code>Authorization: Bearer &lt;key&gt;</code>.
          </p>
          {ownerEmail && <p className="muted">Owner: {ownerEmail}</p>}
        </div>
        <div className="actions">
          <button type="button" className="ghost" onClick={() => navigate('/dashboard')}>
            ← Back to budgets
          </button>
        </div>
      </header>

      <form className="form" onSubmit={submit}>
        <label>
          Key label (optional)
          <input
            type="text"
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="e.g. Claude desktop"
          />
        </label>
        <button type="submit" disabled={createKey.isPending}>
          {createKey.isPending ? 'Creating…' : 'Create API key'}
        </button>
        {createKey.error && <p className="error">Failed to create: {(createKey.error as Error).message}</p>}
      </form>

      {newToken && (
        <div className="key-token">
          <div>
            <p className="eyebrow">New key</p>
            <p className="muted">Copy this key now. You will not be able to see it again.</p>
          </div>
          <div className="key-token__row">
            <code>{newToken.token}</code>
            <button
              type="button"
              className="ghost"
              onClick={async () => {
                await navigator.clipboard.writeText(newToken.token);
              }}
            >
              Copy
            </button>
          </div>
        </div>
      )}

      <div className="key-list">
        <h2>Active keys</h2>
        {keysQuery.isLoading && <p>Loading keys...</p>}
        {keysQuery.error && <p className="error">Failed to load: {(keysQuery.error as Error).message}</p>}
        {!keysQuery.isLoading && !keysQuery.error && keys.length === 0 && <p className="muted">No keys yet.</p>}
        {keys.map((key) => (
          <div key={key.id} className="key-row">
            <div>
              <p className="key-row__title">{key.name || 'Untitled key'}</p>
              <p className="muted">
                {key.prefix} · Created {formatDate(key.created_at)} · Last used {formatDate(key.last_used_at)}
              </p>
            </div>
            <button
              type="button"
              className="ghost danger"
              onClick={() => deleteKey.mutate(key.id)}
              disabled={deleteKey.isPending}
            >
              Revoke
            </button>
          </div>
        ))}
      </div>
    </section>
  );
};

export default APIKeys;
