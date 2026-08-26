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

type Connection = {
  id: number;
  client_id: string;
  client_name: string;
  client_uri?: string;
  scope: string;
  created_at: string;
  last_used_at?: string | null;
  // Absent means the connection does not expire on its own.
  expires_at?: string | null;
};

type ConnectionsResponse = {
  data: Connection[];
  meta: { count: number };
};

type IndexResponse = {
  features?: { receipt_scan?: boolean; oauth?: boolean };
  mcp_url?: string;
};

const formatDate = (value?: string | null) => {
  if (!value) return 'Never';
  const parsed = new Date(value);
  if (Number.isNaN(parsed.valueOf())) return 'Unknown';
  return parsed.toLocaleString();
};

// Expiry is a choice about how long a connection stays valid, so it is offered
// as durations rather than a date picker. "Never" is the default a connection is
// created with; picking it again clears any expiry already set.
const EXPIRY_CHOICES: { label: string; days: number | null }[] = [
  { label: 'Never', days: null },
  { label: '30 days', days: 30 },
  { label: '90 days', days: 90 },
  { label: '1 year', days: 365 }
];

const expiryFromNow = (days: number | null): string | null => {
  if (days === null) return null;
  const when = new Date();
  when.setDate(when.getDate() + days);
  return when.toISOString();
};

const scopeLabel = (scope: string) => {
  const labels: Record<string, string> = {
    'budgets:read': 'Read budgets',
    'budgets:write': 'Add transactions and receipts'
  };
  return scope
    .split(/\s+/)
    .filter(Boolean)
    .map((s) => labels[s] ?? s)
    .join(' · ');
};

const Connections = () => {
  const queryClient = useQueryClient();
  const [name, setName] = useState('');
  const [newToken, setNewToken] = useState<CreateKeyResponse | null>(null);
  const navigate = useNavigate();

  const indexQuery = useQuery({
    queryKey: ['index'],
    queryFn: () => request<IndexResponse>('/api/v1/')
  });
  const oauthEnabled = indexQuery.data?.features?.oauth ?? false;

  const keysQuery = useQuery({
    queryKey: ['api-keys'],
    queryFn: () => request<APIKeysResponse>('/api/v1/api-keys')
  });

  const connectionsQuery = useQuery({
    queryKey: ['connections'],
    queryFn: () => request<ConnectionsResponse>('/api/v1/connections'),
    enabled: oauthEnabled
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

  const disconnect = useMutation({
    mutationFn: (id: number) => request(`/api/v1/connections/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['connections'] });
    }
  });

  const setExpiry = useMutation({
    mutationFn: ({ id, expiresAt }: { id: number; expiresAt: string | null }) =>
      request(`/api/v1/connections/${id}`, { method: 'PATCH', body: { expires_at: expiresAt } }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['connections'] });
    }
  });

  const keys = keysQuery.data?.data ?? [];
  const connections = connectionsQuery.data?.data ?? [];
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
          <h1>Connections</h1>
          <p className="muted">
            Apps that can reach your budgets through the MCP endpoint, either by signing in or with a
            key you paste in yourself.
          </p>
          {ownerEmail && <p className="muted">Owner: {ownerEmail}</p>}
        </div>
        <div className="actions">
          <button type="button" className="ghost" onClick={() => navigate('/dashboard')}>
            ← Back to budgets
          </button>
        </div>
      </header>

      {oauthEnabled && (
        <div className="key-list">
          <h2>Connected apps</h2>
          <p className="muted">
            Apps you signed in to{indexQuery.data?.mcp_url ? ' at ' : ''}
            {indexQuery.data?.mcp_url && <code>{indexQuery.data.mcp_url}</code>}. Disconnecting takes
            effect on the app’s next request.
          </p>
          {connectionsQuery.isLoading && <p>Loading connections...</p>}
          {connectionsQuery.error && (
            <p className="error">Failed to load: {(connectionsQuery.error as Error).message}</p>
          )}
          {!connectionsQuery.isLoading && !connectionsQuery.error && connections.length === 0 && (
            <p className="muted">No apps connected yet.</p>
          )}
          {connections.map((conn) => (
            <div key={conn.id} className="key-row">
              <div>
                <p className="key-row__title">{conn.client_name}</p>
                <p className="muted">
                  {scopeLabel(conn.scope)} · Connected {formatDate(conn.created_at)} · Last used{' '}
                  {formatDate(conn.last_used_at)}
                </p>
                <p className="muted">Expires: {formatDate(conn.expires_at)}</p>
              </div>
              <div className="key-row__actions">
                <label className="key-row__expiry">
                  <span className="eyebrow">Expires</span>
                  <select
                    value={conn.expires_at ? 'custom' : 'never'}
                    disabled={setExpiry.isPending}
                    onChange={(event) => {
                      const choice = EXPIRY_CHOICES.find((c) => c.label === event.target.value);
                      if (!choice) return;
                      setExpiry.mutate({ id: conn.id, expiresAt: expiryFromNow(choice.days) });
                    }}
                  >
                    {/* Shown only while an expiry is set, so the select reflects
                        reality without pretending to know which preset made it. */}
                    {conn.expires_at && <option value="custom">{formatDate(conn.expires_at)}</option>}
                    {EXPIRY_CHOICES.map((choice) => (
                      <option key={choice.label} value={choice.label}>
                        {choice.label}
                      </option>
                    ))}
                  </select>
                </label>
                <button
                  type="button"
                  className="ghost danger"
                  onClick={() => disconnect.mutate(conn.id)}
                  disabled={disconnect.isPending}
                >
                  Disconnect
                </button>
              </div>
            </div>
          ))}
          {(disconnect.error || setExpiry.error) && (
            <p className="error">
              {((disconnect.error ?? setExpiry.error) as Error).message}
            </p>
          )}
        </div>
      )}

      <div className="key-list">
        <h2>API keys</h2>
        <p className="muted">
          For clients that cannot sign in. Authenticate at <code>/mcp</code> with{' '}
          <code>Authorization: Bearer &lt;key&gt;</code>.
        </p>
      </div>

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

export default Connections;
