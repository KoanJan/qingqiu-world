import React from 'react';

export type IconType = 'user' | 'agent' | 'llm' | 'embedding' | 'search' | 'library' | 'kb' | 'exp' | 'custom';

/* ===== Clean geometric SVG icons =====
   Simple shapes using currentColor + opacity for theme consistency. */

// Person: circle head + shoulder arc (Lucide user-round style).
const FlatUserIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <circle cx="12" cy="8" r="5" opacity=".85" />
    <path d="M20 21a8 8 0 0 0-16 0" opacity=".55" />
  </svg>
);

// Agent: two people silhouettes (Lucide users-round style).
const FlatAgentIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M18 21a8 8 0 0 0-16 0" opacity=".55" />
    <circle cx="10" cy="8" r="5" opacity=".85" />
    <path d="M22 20c0-3.37-2-6.5-4-8a5 5 0 0 0-.45-8.3" opacity=".55" />
  </svg>
);

// LLM: brain with circuit connections (Lucide brain-circuit style).
const FlatLLMIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M12 5a3 3 0 1 0-5.997.125 4 4 0 0 0-2.526 5.77 4 4 0 0 0 .556 6.588A4 4 0 1 0 12 18Z" opacity=".85" />
    <path d="M9 13a4.5 4.5 0 0 0 3-4" opacity=".45" />
    <path d="M6.003 5.125A3 3 0 0 0 6.401 6.5" opacity=".45" />
    <path d="M3.477 10.896a4 4 0 0 1 .585-.396" opacity=".45" />
    <path d="M6 18a4 4 0 0 1-1.967-.516" opacity=".45" />
    <path d="M12 13h4" opacity=".55" />
    <path d="M12 18h6a2 2 0 0 1 2 2v1" opacity=".55" />
    <path d="M12 8h8" opacity=".55" />
    <path d="M16 8V5a2 2 0 0 1 2-2" opacity=".55" />
    <circle cx="16" cy="13" r=".5" fill="currentColor" opacity=".7" />
    <circle cx="18" cy="3" r=".5" fill="currentColor" opacity=".7" />
    <circle cx="20" cy="21" r=".5" fill="currentColor" opacity=".7" />
    <circle cx="20" cy="8" r=".5" fill="currentColor" opacity=".7" />
  </svg>
);

// Embedding: triangle of connected nodes.
const FlatEmbeddingIcon = () => (
  <svg viewBox="0 0 24 24" fill="currentColor">
    <line x1="12" y1="6" x2="5" y2="17" stroke="currentColor" strokeWidth="1.5" opacity=".3" />
    <line x1="12" y1="6" x2="19" y2="17" stroke="currentColor" strokeWidth="1.5" opacity=".3" />
    <line x1="5" y1="17" x2="19" y2="17" stroke="currentColor" strokeWidth="1.5" opacity=".3" />
    <circle cx="12" cy="5" r="3" opacity=".85" />
    <circle cx="5" cy="18" r="3" opacity=".6" />
    <circle cx="19" cy="18" r="3" opacity=".6" />
  </svg>
);

// Magnifying glass: ring lens + angled handle.
const FlatSearchIcon = () => (
  <svg viewBox="0 0 24 24" fill="currentColor">
    <circle cx="10.5" cy="10.5" r="6" opacity=".15" />
    <circle cx="10.5" cy="10.5" r="6" fill="none" stroke="currentColor" strokeWidth="1.8" opacity=".85" />
    <line x1="15" y1="15" x2="21" y2="21" stroke="currentColor" strokeWidth="2.5" opacity=".65" strokeLinecap="round" />
  </svg>
);

// Library: four book spines at staggered heights (Lucide library style).
const FlatLibraryIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="m16 6 4 14" opacity=".7" />
    <path d="M12 6v14" opacity=".55" />
    <path d="M8 8v12" opacity=".7" />
    <path d="M4 4v16" opacity=".55" />
  </svg>
);

// KB: open book with text lines (Lucide book-open-text style).
const FlatKBIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M12 7v14" opacity=".55" />
    <path d="M16 12h2" opacity=".45" />
    <path d="M16 8h2" opacity=".45" />
    <path d="M3 18a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h5a4 4 0 0 1 4 4 4 4 0 0 1 4-4h5a1 1 0 0 1 1 1v13a1 1 0 0 1-1 1h-6a3 3 0 0 0-3 3 3 3 0 0 0-3-3z" opacity=".85" />
    <path d="M6 12h2" opacity=".45" />
    <path d="M6 8h2" opacity=".45" />
  </svg>
);

// Exp: notebook with pen (Lucide notebook-pen style).
const FlatExpIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M13.4 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-7.4" opacity=".85" />
    <path d="M2 6h4" opacity=".45" />
    <path d="M2 10h4" opacity=".45" />
    <path d="M2 14h4" opacity=".45" />
    <path d="M2 18h4" opacity=".45" />
    <path d="M21.378 5.626a1 1 0 1 0-3.004-3.004l-5.01 5.012a2 2 0 0 0-.506.854l-.837 2.87a.5.5 0 0 0 .62.62l2.87-.837a2 2 0 0 0 .854-.506z" opacity=".7" />
  </svg>
);

// Interface: palette with swatch (Lucide palette style).
const FlatInterfaceIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <circle cx="13.5" cy="6.5" r=".5" fill="currentColor" opacity=".7" />
    <circle cx="17.5" cy="10.5" r=".5" fill="currentColor" opacity=".7" />
    <circle cx="8.5" cy="7.5" r=".5" fill="currentColor" opacity=".7" />
    <circle cx="6.5" cy="12.5" r=".5" fill="currentColor" opacity=".7" />
    <path d="M12 2C6.5 2 2 6.5 2 12s4.5 10 10 10c.926 0 1.648-.746 1.648-1.688 0-.437-.18-.835-.437-1.125-.29-.289-.438-.652-.438-1.125a1.64 1.64 0 0 1 1.668-1.668h1.996c3.051 0 5.555-2.503 5.555-5.554C21.965 6.012 17.461 2 12 2z" opacity=".85" />
  </svg>
);

/* ===== Icon map ===== */

interface IconConfig {
  icon: React.ReactNode;
  colorVar: string;
  bgVar: string;
}

const ICON_MAP: Record<IconType, IconConfig> = {
  user:    { icon: <FlatUserIcon />,        colorVar: 'var(--color-primary)',    bgVar: 'var(--color-primary-bg)' },
  agent:   { icon: <FlatAgentIcon />,       colorVar: 'var(--color-agent)',      bgVar: 'var(--color-agent-bg)' },
  llm:     { icon: <FlatLLMIcon />,         colorVar: 'var(--color-llm)',        bgVar: 'var(--color-llm-bg)' },
  embedding:{ icon: <FlatEmbeddingIcon />,  colorVar: 'var(--color-embedding)',  bgVar: 'var(--color-embedding-bg)' },
  search:  { icon: <FlatSearchIcon />,      colorVar: 'var(--color-search)',     bgVar: 'var(--color-search-bg)' },
  library: { icon: <FlatLibraryIcon />,     colorVar: 'var(--color-kb)',         bgVar: 'var(--color-kb-bg)' },
  kb:      { icon: <FlatKBIcon />,          colorVar: 'var(--color-kb)',         bgVar: 'var(--color-kb-bg)' },
  exp:     { icon: <FlatExpIcon />,         colorVar: 'var(--color-kb)',         bgVar: 'var(--color-kb-bg)' },
  custom:    { icon: <FlatInterfaceIcon />, colorVar: 'var(--color-primary)', bgVar: 'var(--color-primary-bg)' },
};

/* ===== ConfigIcon component ===== */

interface ConfigIconProps {
  type: IconType;
  size?: number;
  iconSize?: number;
  borderRadius?: string;
  marginBottom?: number;
}

const ConfigIcon: React.FC<ConfigIconProps> = ({
  type,
  size = 36,
  iconSize = 16,
  borderRadius = '8px',
  marginBottom,
}) => {
  const config = ICON_MAP[type];

  return (
    <div
      style={{
        width: size,
        height: size,
        borderRadius,
        backgroundColor: config.bgVar,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        color: config.colorVar,
        fontSize: iconSize,
        flexShrink: 0,
        marginBottom,
      }}
    >
      <span style={{ width: '70%', height: '70%' }}>{config.icon}</span>
    </div>
  );
};

export default ConfigIcon;
