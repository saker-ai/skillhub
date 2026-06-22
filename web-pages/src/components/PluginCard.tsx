import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { Plugin } from '@saker/skillhub-client/plugins';

interface Props {
  plugin: Plugin;
}

export default function PluginCard({ plugin }: Props) {
  const { t } = useTranslation();
  const title = plugin.displayName || plugin.slug;

  return (
    <Link to={`/plugins/${plugin.slug}`} className="card">
      <div className="card-header">
        <h3 className="card-title">{title}</h3>
        <span className="card-category">{plugin.category}</span>
      </div>
      <p className="card-slug">@{plugin.ownerHandle}/{plugin.slug}</p>
      {plugin.summary && <p className="card-summary">{plugin.summary}</p>}
      {plugin.tags && plugin.tags.length > 0 && (
        <div className="card-tags">
          {plugin.tags.slice(0, 3).map(tag => (
            <span key={tag} className="tag">{tag}</span>
          ))}
        </div>
      )}
      <div className="card-meta">
        <span title={t('detail.downloads')}>&#8615; {plugin.downloads}</span>
        <span title={t('detail.stars')}>&#9733; {plugin.starsCount}</span>
      </div>
    </Link>
  );
}
