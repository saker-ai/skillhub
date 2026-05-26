import { useEffect, useState, useCallback } from 'react';
import { BrowserRouter, Routes, Route } from 'react-router-dom';
import { AuthContext } from './hooks/useAuth';
import { whoami, type User } from './api/auth';
import Layout from './components/Layout';
import Home from './pages/Home';
import Skills from './pages/Skills';
import SkillDetail from './pages/SkillDetail';
import Search from './pages/Search';
import Publish from './pages/Publish';
import Login from './pages/Login';
import Namespaces from './pages/Namespaces';
import NamespaceDetail from './pages/NamespaceDetail';
import Plugins from './pages/Plugins';
import PluginDetail from './pages/PluginDetail';

const basename = import.meta.env.BASE_URL.replace(/\/$/, '') || '/';

export default function App() {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    setLoading(true);
    const u = await whoami();
    setUser(u);
    setLoading(false);
  }, []);

  useEffect(() => { refresh(); }, [refresh]);

  return (
    <AuthContext.Provider value={{ user, loading, refresh }}>
      <BrowserRouter basename={basename}>
        <Routes>
          <Route element={<Layout />}>
            <Route path="/" element={<Home />} />
            <Route path="/skills" element={<Skills />} />
            <Route path="/skills/:slug" element={<SkillDetail />} />
            <Route path="/search" element={<Search />} />
            <Route path="/publish" element={<Publish />} />
            <Route path="/login" element={<Login />} />
            <Route path="/namespaces" element={<Namespaces />} />
            <Route path="/namespaces/:slug" element={<NamespaceDetail />} />
            <Route path="/plugins" element={<Plugins />} />
            <Route path="/plugins/:slug" element={<PluginDetail />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </AuthContext.Provider>
  );
}
