import React, { useRef, useState, useEffect } from 'react';
import { Slider, Button, message, Popconfirm, Modal } from 'antd';
import { useTranslation } from 'react-i18next';
import { DeleteOutlined, UploadOutlined, PlusOutlined, ZoomInOutlined } from '@ant-design/icons';
import type { BgMode } from '../hooks/useAppearance';
import { COLOR_PRESETS, getBackgroundUrl, PRESET_BACKGROUNDS } from '../hooks/useAppearance';

// ---- types ----

interface AppearancePanelProps {
  /** Current UI language. */
  language: string;
  /** Current background mode. */
  bgMode: BgMode;
  /** Current hex colour (colour mode). */
  bgColor: string;
  /** Current background image identifier. */
  bgImage: string;
  /** Source type of bgImage — "preset" or "upload". */
  bgImageSource: string;
  /** User-added custom colours. */
  customColors: string[];
  /** Glass overlay opacity, 0-1. */
  glassOpacity: number;
  /** Glass blur radius in px. */
  glassBlur: number;
  /** Change UI language — persists to config.json + applies i18n. */
  onLanguageChange: (lang: string) => void;
  /** Select a colour — atomically sets mode + colour, triggers re-render. */
  onSelectColor: (color: string) => void;
  onAddCustomColor: (color: string) => void;
  onRemoveCustomColor: (color: string) => void;
  /** Select a background image — atomically sets mode + image + source, triggers re-render. */
  onSelectBgImage: (value: string, source: 'preset' | 'upload') => void;
  onUploadBgImage: (dataUrl: string) => void;
  onDeleteUserImage: (filename: string) => void;
  fetchUserImages: () => Promise<string[]>;
  onGlassOpacityChange: (value: number) => void;
  onGlassBlurChange: (value: number) => void;
  /** Reset background settings (mode, colour, image, custom colours). */
  onResetBackground: () => void;
  /** Reset overlay settings (opacity, blur). */
  onResetOverlay: () => void;
}

// ---- preview modal ----

const PreviewModal: React.FC<{
  visible: boolean;
  imageUrl: string;
  onClose: () => void;
}> = ({ visible, imageUrl, onClose }) => (
  <Modal
    open={visible}
    footer={null}
    onCancel={onClose}
    width="80vw"
    style={{ top: 24 }}
    styles={{ body: { padding: 12, display: 'flex', justifyContent: 'center', alignItems: 'center' } }}
  >
    <img
      src={imageUrl}
      alt="Preview"
      style={{ maxWidth: '100%', maxHeight: '70vh', objectFit: 'contain', borderRadius: 8 }}
    />
  </Modal>
);

// ---- shared image gallery ----

interface GalleryImage {
  /** Unique key for React and selection comparison. */
  key: string;
  /** URL to display the thumbnail / full image. */
  url: string;
  /** Whether this image is currently selected as the background. */
  selected: boolean;
  /** If provided, a delete action is shown on hover. */
  onDelete?: () => void;
}

/**
 * Shared gallery grid used by both system presets and user-uploaded images.
 * Renders cards with a thumbnail, magnifying-glass zoom button, and optional delete.
 */
const ImageGallery: React.FC<{
  images: GalleryImage[];
  onSelect: (key: string) => void;
}> = ({ images, onSelect }) => {
  const { t } = useTranslation();
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);

  return (
    <>
      <div className="appearance-image-grid">
        {images.map((img) => (
          <div key={img.key} className="appearance-image-card-wrapper">
            <button
              type="button"
              className={`appearance-image-card${img.selected ? ' selected' : ''}`}
              onClick={() => onSelect(img.key)}
            >
              <div
                className="appearance-image-thumb"
                style={{ backgroundImage: `url(${img.url})` }}
              />
            </button>
            <div className="appearance-image-actions">
              <button
                type="button"
                className="appearance-image-zoom"
                title="Zoom"
                onClick={(e) => {
                  e.stopPropagation();
                  setPreviewUrl(img.url);
                }}
              >
                <ZoomInOutlined />
              </button>
              {img.onDelete && (
                <Popconfirm
                  title={t('appearance.confirmDeleteImage')}
                  onConfirm={(e) => { e?.stopPropagation(); img.onDelete?.(); }}
                  okText={t('common.delete')}
                  cancelText={t('common.cancel')}
                >
                  <button
                    type="button"
                    className="appearance-image-delete"
                    title={t('appearance.deleteImage')}
                    onClick={(e) => e.stopPropagation()}
                  >
                    <DeleteOutlined />
                  </button>
                </Popconfirm>
              )}
            </div>
          </div>
        ))}
      </div>
      <PreviewModal
        visible={previewUrl !== null}
        imageUrl={previewUrl ?? ''}
        onClose={() => setPreviewUrl(null)}
      />
    </>
  );
};

// ---- shared swatch button ----

interface SwatchButtonProps {
  color: string;
  selected: boolean;
  onSelect: (color: string) => void;
  /** If provided, shows a delete action with Popconfirm on hover. */
  onDelete?: (color: string) => void;
  deleteConfirmTitle?: string;
  deleteTitle?: string;
}

const SwatchButton: React.FC<SwatchButtonProps> = ({
  color, selected, onSelect, onDelete, deleteConfirmTitle, deleteTitle,
}) => {
  const { t } = useTranslation();
  const btn = (
    <button
      type="button"
      className={`appearance-swatch${selected ? ' selected' : ''}`}
      style={{ backgroundColor: color }}
      title={color}
      onClick={() => onSelect(color)}
    />
  );
  if (!onDelete) return btn;
  return (
    <div className="appearance-swatch-wrapper">
      {btn}
      <div className="appearance-swatch-actions">
        <Popconfirm
          title={deleteConfirmTitle}
          onConfirm={(e) => { e?.stopPropagation(); onDelete(color); }}
          okText={t('common.delete')}
          cancelText={t('common.cancel')}
        >
          <button
            type="button"
            className="appearance-swatch-remove"
            title={deleteTitle}
            onClick={(e) => e.stopPropagation()}
          >
            <DeleteOutlined />
          </button>
        </Popconfirm>
      </div>
    </div>
  );
};

// ---- colour panel ----

const ColorPanel: React.FC<{
  bgColor: string;
  customColors: string[];
  onSelectColor: (color: string) => void;
  onAddCustomColor: (color: string) => void;
  onRemoveCustomColor: (color: string) => void;
}> = ({ bgColor, customColors, onSelectColor, onAddCustomColor, onRemoveCustomColor }) => {
  const { t } = useTranslation();
  const [pickerColor, setPickerColor] = useState('#f3f4f6');

  return (
    <div className="appearance-tab-body">
      {/* 5 preset colour swatches */}
      <div className="appearance-section">
        <label className="appearance-label">{t('appearance.colorPresets')}</label>
        <div className="appearance-swatch-grid">
          {COLOR_PRESETS.map((color) => (
            <SwatchButton
              key={color}
              color={color}
              selected={bgColor === color}
              onSelect={onSelectColor}
            />
          ))}
        </div>
      </div>

      {/* My colours — header: label + picker row; body: swatches or empty */}
      <div className="appearance-section">
        <div className="appearance-section-header">
          <label className="appearance-label">{t('appearance.myColors')}</label>
          <div className="appearance-color-picker-row">
            <input
              type="color"
              value={pickerColor}
              onChange={(e) => setPickerColor(e.target.value)}
              className="appearance-color-picker"
            />
            <span className="appearance-color-hex">{pickerColor}</span>
            <Button
              size="small"
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => {
                onAddCustomColor(pickerColor);
                message.success(t('appearance.colorAdded'));
              }}
            >
              {t('appearance.addColor')}
            </Button>
          </div>
        </div>
        {customColors.length === 0 ? (
          <div className="appearance-grid-empty">{t('appearance.noCustomColors')}</div>
        ) : (
          <div className="appearance-swatch-grid">
            {customColors.map((color) => (
              <SwatchButton
                key={color}
                color={color}
                selected={bgColor === color}
                onSelect={onSelectColor}
                onDelete={onRemoveCustomColor}
                deleteConfirmTitle={t('appearance.confirmDeleteColor')}
                deleteTitle={t('appearance.removeColor')}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  );
};

// ---- image panel ----

const ImagePanel: React.FC<{
  bgImage: string;
  bgImageSource: string;
  onSelectBgImage: (value: string, source: 'preset' | 'upload') => void;
  onUploadBgImage: (dataUrl: string) => void;
  onDeleteUserImage: (filename: string) => void;
  fetchUserImages: () => Promise<string[]>;
}> = ({ bgImage, bgImageSource, onSelectBgImage, onUploadBgImage, onDeleteUserImage, fetchUserImages }) => {
  const { t } = useTranslation();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [userImages, setUserImages] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);

  const currentPreset = bgImageSource === 'preset' ? bgImage : null;
  const currentFilename = (bgImageSource === 'upload' && bgImage && !bgImage.startsWith('data:'))
    ? bgImage
    : null;

  useEffect(() => {
    fetchUserImages()
      .then(setUserImages)
      .finally(() => setLoading(false));
  }, [fetchUserImages]);

  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    if (!file.type.startsWith('image/')) {
      message.error(t('appearance.invalidImageType'));
      return;
    }

    const reader = new FileReader();
    reader.onload = () => {
      onUploadBgImage(reader.result as string);
      setTimeout(() => {
        fetchUserImages().then(setUserImages);
      }, 500);
    };
    reader.readAsDataURL(file);
    if (fileInputRef.current) fileInputRef.current.value = '';
  };

  const handleDelete = (filename: string) => {
    onDeleteUserImage(filename);
    setUserImages((prev) => prev.filter((f) => f !== filename));
  };

  // Build gallery arrays
  const presetImages: GalleryImage[] = PRESET_BACKGROUNDS.map((entry) => ({
    key: entry.filename,
    url: entry.url,
    selected: currentPreset === entry.filename,
  }));

  const uploadImages: GalleryImage[] = userImages.map((filename) => ({
    key: filename,
    url: getBackgroundUrl(filename, 'upload') ?? '',
    selected: currentFilename === filename,
    onDelete: () => handleDelete(filename),
  }));

  return (
    <div className="appearance-tab-body">
      {/* System presets */}
      <div className="appearance-section">
        <label className="appearance-label">{t('appearance.systemPresets')}</label>
        <ImageGallery images={presetImages} onSelect={(key) => onSelectBgImage(key, 'preset')} />
      </div>

      {/* User uploads */}
      <div className="appearance-section">
        <div className="appearance-section-header">
          <label className="appearance-label">{t('appearance.myImages')}</label>
          <Button
            size="small"
            type="primary"
            icon={<UploadOutlined />}
            onClick={() => fileInputRef.current?.click()}
          >
            {t('appearance.uploadImage')}
          </Button>
        </div>
        <input
          ref={fileInputRef}
          type="file"
          accept="image/*"
          style={{ display: 'none' }}
          onChange={handleFileChange}
        />
        {loading ? (
          <div className="appearance-grid-loading">{t('common.loading')}</div>
        ) : userImages.length === 0 ? (
          <div className="appearance-grid-empty">{t('appearance.noUserImages')}</div>
        ) : (
          <ImageGallery images={uploadImages} onSelect={(key) => onSelectBgImage(key, 'upload')} />
        )}
      </div>
    </div>
  );
};

// ---- nav item keys ----

type SubNav = 'language' | 'background' | 'overlay';

// ---- main panel ----

/**
 * Appearance settings panel with a VS Code-style two-pane layout:
 * a left navigation column and a right content detail area.
 *
 * The nav has two top-level sections:
 *  1. Language    — standalone item
 *  2. Appearance  — expandable group with Background / Overlay sub-items
 */
const AppearancePanel: React.FC<AppearancePanelProps> = (props) => {
  const { t } = useTranslation();

  // Sub-nav selection — one of the three leaf pages.
  const [subNav, setSubNav] = useState<SubNav>('language');
  // Whether the "Appearance" group is expanded.
  const [appearanceExpanded, setAppearanceExpanded] = useState(true);

  // Local bg-mode tab state: freely switchable without triggering a save.
  const [activeTab, setActiveTab] = useState<BgMode>(props.bgMode);
  useEffect(() => {
    setActiveTab(props.bgMode);
  }, [props.bgMode]);

  // Local slider state for instant visual feedback during drag.
  // Persistence only fires onAfterChange (mouse-up).
  const [localOpacity, setLocalOpacity] = useState(props.glassOpacity);
  const [localBlur, setLocalBlur] = useState(props.glassBlur);
  useEffect(() => { setLocalOpacity(props.glassOpacity); }, [props.glassOpacity]);
  useEffect(() => { setLocalBlur(props.glassBlur); }, [props.glassBlur]);

  /** True when either Background or Overlay sub-item is active. */
  const isAppearanceGroupActive = subNav === 'background' || subNav === 'overlay';

  return (
    <div className="appearance-panel">
      {/* Left navigation */}
      <nav className="appearance-nav">
        {/* Language — standalone item */}
        <button
          type="button"
          className={`appearance-nav-item${subNav === 'language' ? ' active' : ''}`}
          onClick={() => setSubNav('language')}
        >
          {t('settings.language')}
        </button>

        {/* Appearance — expandable group */}
        <button
          type="button"
          className={`appearance-nav-group${isAppearanceGroupActive ? ' active' : ''}`}
          onClick={() => setAppearanceExpanded(prev => !prev)}
        >
          <span>{t('appearance.title')}</span>
          <span className="appearance-nav-chevron">
            {appearanceExpanded ? '▾' : '▸'}
          </span>
        </button>

        {appearanceExpanded && (
          <>
            <button
              type="button"
              className={`appearance-nav-item appearance-nav-sub${subNav === 'background' ? ' active' : ''}`}
              onClick={() => setSubNav('background')}
            >
              {t('appearance.background')}
            </button>
            <button
              type="button"
              className={`appearance-nav-item appearance-nav-sub${subNav === 'overlay' ? ' active' : ''}`}
              onClick={() => setSubNav('overlay')}
            >
              {t('appearance.overlay')}
            </button>
          </>
        )}
      </nav>

      {/* Right content */}
      <div className="appearance-content">
        {subNav === 'language' ? (
          /* ---- Language ---- */
          <div className="appearance-section">
            <h3 className="appearance-section-title">{t('settings.language')}</h3>
            <div className="lang-options">
              <div
                className={`lang-option-card${props.language === 'zh' ? ' active' : ''}`}
                onClick={() => props.onLanguageChange('zh')}
              >
                <span className="lang-option-text">中文</span>
              </div>
              <div
                className={`lang-option-card${props.language === 'en' ? ' active' : ''}`}
                onClick={() => props.onLanguageChange('en')}
              >
                <span className="lang-option-text">English</span>
              </div>
            </div>
          </div>
        ) : subNav === 'background' ? (
          /* ---- Background ---- */
          <div className="appearance-subsection">
            <div className="appearance-subsection-header">
              <h3 className="appearance-section-title">{t('appearance.background')}</h3>
              <Button size="small" onClick={props.onResetBackground}>
                {t('appearance.resetBackground')}
              </Button>
            </div>

            <div className="appearance-mode-tabs">
              <button
                type="button"
                className={`appearance-mode-tab${activeTab === 'color' ? ' active' : ''}`}
                onClick={() => setActiveTab('color')}
              >
                {t('appearance.bgColor')}
              </button>
              <button
                type="button"
                className={`appearance-mode-tab${activeTab === 'image' ? ' active' : ''}`}
                onClick={() => setActiveTab('image')}
              >
                {t('appearance.bgImage')}
              </button>
            </div>

            {activeTab === 'color' ? (
              <ColorPanel
                bgColor={props.bgColor}
                customColors={props.customColors}
                onSelectColor={props.onSelectColor}
                onAddCustomColor={props.onAddCustomColor}
                onRemoveCustomColor={props.onRemoveCustomColor}
              />
            ) : (
              <ImagePanel
                bgImage={props.bgImage}
                bgImageSource={props.bgImageSource}
                onSelectBgImage={props.onSelectBgImage}
                onUploadBgImage={props.onUploadBgImage}
                onDeleteUserImage={props.onDeleteUserImage}
                fetchUserImages={props.fetchUserImages}
              />
            )}
          </div>
        ) : (
          /* ---- Overlay ---- */
          <div className="appearance-subsection">
            <div className="appearance-subsection-header">
              <h3 className="appearance-section-title">{t('appearance.overlay')}</h3>
              <Button size="small" onClick={props.onResetOverlay}>
                {t('appearance.resetOverlay')}
              </Button>
            </div>

            <div className="appearance-section">
              <label className="appearance-label">
                {t('appearance.glassOpacity')} ({Math.round(localOpacity * 100)}%)
              </label>
              <Slider
                min={0}
                max={1}
                step={0.01}
                value={localOpacity}
                onChange={(v) => {
                  const val = v as number;
                  setLocalOpacity(val);
                  document.documentElement.style.setProperty('--glass-opacity', String(val));
                }}
                onAfterChange={(v) => props.onGlassOpacityChange(v as number)}
                tooltip={{ formatter: (v) => `${Math.round((v ?? 0) * 100)}%` }}
              />
              <div className="appearance-slider-hint">
                <span>{t('appearance.transparent')}</span>
                <span>{t('appearance.opaque')}</span>
              </div>
            </div>

            <div className="appearance-section">
              <label className="appearance-label">
                {t('appearance.glassBlur')} ({localBlur}px)
              </label>
              <Slider
                min={0}
                max={20}
                step={1}
                value={localBlur}
                onChange={(v) => {
                  const val = v as number;
                  setLocalBlur(val);
                  document.documentElement.style.setProperty('--glass-blur', `${val}px`);
                }}
                onAfterChange={(v) => props.onGlassBlurChange(v as number)}
                tooltip={{ formatter: (v) => `${v}px` }}
              />
              <div className="appearance-slider-hint">
                <span>{t('appearance.clear')}</span>
                <span>{t('appearance.blurry')}</span>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
};

export default AppearancePanel;
