import { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuth } from '../hooks/useAuth';
import {
  listMyNamespaces,
  createNamespace,
  type Namespace,
} from '@saker/skillhub-client/namespaces';

export default function Namespaces() {
  const { t } = useTranslation();
  const { user, loading: authLoading } = useAuth();
  const navigate = useNavigate();

  const [namespaces, setNamespaces] = useState<Namespace[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [showCreate, setShowCreate] = useState(false);

  // Create-form state
  const [slug, setSlug] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [description, setDescription] = useState('');
  const [creating, setCreating] = useState(false);

  useEffect(() => {
    // Redirect to login while preserving intent — the page is meaningless
    // without an identity.
    if (!authLoading && !user) {
      navigate('/login?redirect=/namespaces', { replace: true });
    }
  }, [authLoading, user, navigate]);

  const refresh = async () => {
    setLoading(true);
    setError('');
    try {
      const res = await listMyNamespaces();
      setNamespaces(res.data || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load namespaces');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (user) refresh();
  }, [user]);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!slug.trim()) return;
    setCreating(true);
    setError('');
    try {
      await createNamespace({
        slug: slug.trim(),
        displayName: displayName.trim() || undefined,
        description: description.trim() || undefined,
        type: 'team',
      });
      setShowCreate(false);
      setSlug('');
      setDisplayName('');
      setDescription('');
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create namespace');
    } finally {
      setCreating(false);
    }
  };

  if (authLoading || !user) {
    return <div className="container">{t('common.loading')}</div>;
  }

  return (
    <div className="container" style={{ paddingTop: '2rem', paddingBottom: '4rem' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <h1 style={{ margin: 0 }}>{t('namespaces.title')}</h1>
        <button className="btn btn-primary" onClick={() => setShowCreate(v => !v)}>
          {showCreate ? t('common.cancel') : t('namespaces.create_btn')}
        </button>
      </div>

      {error && <div className="error-box" style={{ marginBottom: '1rem' }}>{error}</div>}

      {showCreate && (
        <form onSubmit={handleCreate} style={{
          background: 'var(--surface, #fff)',
          padding: '1.5rem',
          borderRadius: '8px',
          marginBottom: '1.5rem',
          border: '1px solid var(--border, #e5e7eb)',
        }}>
          <h2 style={{ marginTop: 0 }}>{t('namespaces.create_title')}</h2>
          <div style={{ display: 'grid', gap: '1rem' }}>
            <div>
              <label className="form-label">{t('namespaces.slug')} *</label>
              <input
                type="text"
                className="form-input"
                value={slug}
                onChange={e => setSlug(e.target.value)}
                placeholder="acme-team"
                required
                pattern="[a-z0-9][a-z0-9-]*[a-z0-9]"
                title={t('namespaces.slug_hint') || ''}
              />
              <small style={{ color: 'var(--muted, #6b7280)' }}>{t('namespaces.slug_hint')}</small>
            </div>
            <div>
              <label className="form-label">{t('namespaces.display_name')}</label>
              <input
                type="text"
                className="form-input"
                value={displayName}
                onChange={e => setDisplayName(e.target.value)}
                placeholder="Acme Team"
              />
            </div>
            <div>
              <label className="form-label">{t('namespaces.description')}</label>
              <textarea
                className="form-input"
                value={description}
                onChange={e => setDescription(e.target.value)}
                rows={2}
              />
            </div>
            <div>
              <button type="submit" className="btn btn-primary" disabled={creating || !slug.trim()}>
                {creating ? t('common.loading') : t('namespaces.create_submit')}
              </button>
            </div>
          </div>
        </form>
      )}

      {loading ? (
        <div>{t('common.loading')}</div>
      ) : namespaces.length === 0 ? (
        <div style={{ color: 'var(--muted, #6b7280)' }}>{t('namespaces.empty')}</div>
      ) : (
        <div style={{ display: 'grid', gap: '0.75rem' }}>
          {namespaces.map(ns => (
            <Link
              key={ns.id}
              to={`/namespaces/${ns.slug}`}
              style={{
                display: 'block',
                background: 'var(--surface, #fff)',
                padding: '1rem 1.25rem',
                borderRadius: '8px',
                border: '1px solid var(--border, #e5e7eb)',
                textDecoration: 'none',
                color: 'inherit',
              }}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
                <strong>{ns.displayName || ns.slug}</strong>
                <span style={{ fontSize: '0.85rem', color: 'var(--muted, #6b7280)' }}>
                  {ns.type} · {ns.status}
                </span>
              </div>
              <div style={{ color: 'var(--muted, #6b7280)', fontSize: '0.9rem', marginTop: '0.25rem' }}>
                @{ns.slug}
              </div>
              {ns.description && (
                <div style={{ marginTop: '0.5rem', fontSize: '0.95rem' }}>{ns.description}</div>
              )}
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
