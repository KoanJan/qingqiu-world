import React from 'react';
import { EditOutlined, DeleteOutlined } from '@ant-design/icons';

// ---- shared card action buttons (glass style, bottom-right, hover reveal) ----

interface CardActionsProps {
  /** Edit button click handler. If omitted, the edit button is hidden. */
  onEdit?: (e: React.MouseEvent) => void;
  /** Delete button click handler. If omitted, the delete button is hidden. */
  onDelete?: (e: React.MouseEvent) => void;
  /** Optional extra action buttons rendered before edit/delete. */
  children?: React.ReactNode;
}

/**
 * Shared glass-style action buttons for item cards.
 * Positioned absolutely at the card's bottom-right corner,
 * revealed on hover with semi-transparent background + blur.
 *
 * Used by: Agent cards, KB cards, LLM/embedding/search config cards,
 * public experience cards.
 */
const CardActions: React.FC<CardActionsProps> = ({ onEdit, onDelete, children }) => (
  <div className="item-card-actions">
    {children}
    {onEdit && (
      <button className="item-card-action-btn" onClick={onEdit}>
        <EditOutlined />
      </button>
    )}
    {onDelete && (
      <button className="item-card-action-btn item-card-action-delete" onClick={onDelete}>
        <DeleteOutlined />
      </button>
    )}
  </div>
);

export default CardActions;
