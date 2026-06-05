import { useState, useEffect, useCallback } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { listPlugins, type Plugin } from '../api/plugins';
import PluginCard from '../components/PluginCard';

const PAGE_SIZE = 20;

export default function Plugins() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const [loading, setLoading] = useState(true);
  const [cursors, setCursors] = useState<string[]>(['']);
  const [currentPage, setCurrentPage] = useState(0);
  const [hasNext, setHasNext] = useState(false);

  const sort = searchParams.get('sort') || 'created';

  const loadPage = useCallback(async (page: number, cursor: string) => {
    setLoading(true);
    try {
      const res = await listPlugins(PAGE_SIZE, cursor, '', sort);
      setPlugins(res.data || []);
      setHasNext(!!res.nextCursor);
      if (res.nextCursor) {
        setCursors(prev => {
          if (page < prev.length - 1) return prev;
          const next = [...prev];
          next[page + 1] = res.nextCursor;
          return next;
        });
      }
    } catch {
      setPlugins([]);
    } finally {
      setLoading(false);
    }
  }, [sort]);

  useEffect(() => {
    loadPage(0, '');
  }, [loadPage]);

  const goToPage = (page: number) => {
    setCurrentPage(page);
    loadPage(page, cursors[page] || '');
  };

  const setSort = (s: string) => {
    setSearchParams({ sort: s });
    setCursors(['']);
    setCurrentPage(0);
  };

  return (
    <section style={{ padding: '40px 0' }}>
      <div className="container">
        <h1 style={{ fontSize: '1.75rem', fontWeight: 700, marginBottom: 20 }}>{t('plugins.title')}</h1>

        <div className="filter-bar">
          {['created', 'downloads', 'stars', 'name'].map(s => (
            <button
              key={s}
              className={`filter-btn${sort === s ? ' active' : ''}`}
              onClick={() => setSort(s)}
            >
              {t(`plugins.${s === 'created' ? 'newest' : s}`)}
            </button>
          ))}
        </div>

        {loading ? (
          <div style={{ textAlign: 'center', padding: '64px 0' }}>
            <div className="spinner" />
          </div>
        ) : plugins.length === 0 ? (
          <div style={{ textAlign: 'center', padding: '64px 0', color: 'var(--text-muted)' }}>
            <p style={{ fontSize: '1.1rem' }}>{t('plugins.no_plugins')}</p>
            <p style={{ marginTop: 8 }}>{t('plugins.be_first')}</p>
          </div>
        ) : (
          <>
            <div className="skill-grid">
              {plugins.map(p => (
                <PluginCard key={p.id} plugin={p} />
              ))}
            </div>
            {(currentPage > 0 || hasNext) && (
              <div className="pagination">
                <button
                  className="pagination-btn"
                  disabled={currentPage === 0}
                  onClick={() => goToPage(currentPage - 1)}
                >
                  &larr;
                </button>
                <span className="pagination-info">{currentPage + 1}</span>
                <button
                  className="pagination-btn"
                  disabled={!hasNext}
                  onClick={() => goToPage(currentPage + 1)}
                >
                  &rarr;
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </section>
  );
}
